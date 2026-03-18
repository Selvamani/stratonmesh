// Package snapshot provides stateful volume snapshotting and restore for StratonMesh stacks.
//
// Architecture:
//   - Snapshots are stored in MinIO (S3-compatible) under the "stratonmesh-snapshots" bucket.
//   - Each snapshot captures all named Docker volumes belonging to a stack, plus the stack manifest.
//   - Object key format: snapshots/{stackID}/{snapshotID}/{volumeName}.tar.gz
//                         snapshots/{stackID}/{snapshotID}/manifest.json
//   - Snapshot metadata is persisted in etcd under /stratonmesh/snapshots/{stackID}/{snapshotID}.
//
// Use cases:
//   - Disaster recovery:     restore a stack from the last known-good snapshot.
//   - Stack cloning:         create a new stack from a snapshot (preview environments).
//   - Version branching:     snapshot before a risky migration, restore if it fails.
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/store"
	"go.uber.org/zap"
)

const (
	defaultBucket = "stratonmesh-snapshots"
	// etcd key prefix for snapshot metadata
	snapshotPrefix = "snapshots"
)

// Config holds MinIO connection settings.
type Config struct {
	Endpoint        string `yaml:"endpoint" json:"endpoint"`               // e.g. "localhost:9000"
	AccessKeyID     string `yaml:"accessKeyId" json:"accessKeyId"`
	SecretAccessKey string `yaml:"secretAccessKey" json:"secretAccessKey"`
	UseSSL          bool   `yaml:"useSSL" json:"useSSL"`
	Bucket          string `yaml:"bucket" json:"bucket"`                   // default: "stratonmesh-snapshots"
}

// Snapshot describes a point-in-time backup of a stack's volumes.
type Snapshot struct {
	ID          string            `json:"id"`
	StackID     string            `json:"stackId"`
	Label       string            `json:"label,omitempty"` // human-readable tag
	Volumes     []VolumeSnap      `json:"volumes"`
	ManifestVer string            `json:"manifestVersion"`
	CreatedAt   time.Time         `json:"createdAt"`
	SizeBytes   int64             `json:"sizeBytes"`
	Status      string            `json:"status"` // creating | ready | failed | deleted
	Error       string            `json:"error,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// VolumeSnap describes one volume captured in a snapshot.
type VolumeSnap struct {
	Name      string `json:"name"`       // Docker volume name
	ObjectKey string `json:"objectKey"`  // path inside the bucket
	SizeBytes int64  `json:"sizeBytes"`
}

// Engine manages snapshot operations.
type Engine struct {
	cfg    Config
	mc     *minio.Client
	store  *store.Store
	logger *zap.SugaredLogger
}

// New creates a snapshot Engine.
func New(cfg Config, st *store.Store, logger *zap.SugaredLogger) (*Engine, error) {
	if cfg.Bucket == "" {
		cfg.Bucket = defaultBucket
	}

	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to minio: %w", err)
	}

	return &Engine{cfg: cfg, mc: mc, store: st, logger: logger}, nil
}

// EnsureBucket creates the snapshot bucket if it doesn't exist.
func (e *Engine) EnsureBucket(ctx context.Context) error {
	exists, err := e.mc.BucketExists(ctx, e.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := e.mc.MakeBucket(ctx, e.cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("create bucket %q: %w", e.cfg.Bucket, err)
		}
		e.logger.Infow("created snapshot bucket", "bucket", e.cfg.Bucket)
	}
	return nil
}

// Create takes a snapshot of all named volumes for the given stack.
// It runs a temporary alpine container to tar each volume and stream it to MinIO.
func (e *Engine) Create(ctx context.Context, stackID string, label string, tags map[string]string) (*Snapshot, error) {
	if err := e.EnsureBucket(ctx); err != nil {
		return nil, err
	}

	snapshotID := uuid.New().String()[:8] // short ID for readability
	snap := &Snapshot{
		ID:        snapshotID,
		StackID:   stackID,
		Label:     label,
		Status:    "creating",
		CreatedAt: time.Now(),
		Tags:      tags,
	}

	// Read the manifest to know which volumes to snapshot.
	var desired manifest.Stack
	if err := e.store.GetDesired(ctx, stackID, &desired); err != nil {
		return nil, fmt.Errorf("stack %q not found: %w", stackID, err)
	}
	snap.ManifestVer = desired.Version

	// Save initial metadata so callers can poll status.
	if err := e.saveSnapshot(ctx, snap); err != nil {
		e.logger.Warnw("save snapshot meta failed", "error", err)
	}

	// Collect all named volumes for this stack.
	volumes := collectVolumes(&desired)
	if len(volumes) == 0 {
		snap.Status = "ready"
		snap.Volumes = []VolumeSnap{}
		e.saveSnapshot(ctx, snap) //nolint:errcheck
		e.logger.Infow("snapshot created (no volumes)", "stack", stackID, "snapshot", snapshotID)
		return snap, nil
	}

	// Also upload the stack manifest as JSON.
	manifestKey := fmt.Sprintf("snapshots/%s/%s/manifest.json", stackID, snapshotID)
	manifestData, _ := json.Marshal(desired)
	if _, err := e.mc.PutObject(ctx, e.cfg.Bucket, manifestKey,
		bytes.NewReader(manifestData), int64(len(manifestData)),
		minio.PutObjectOptions{ContentType: "application/json"},
	); err != nil {
		e.logger.Warnw("upload manifest failed", "error", err)
	}

	// Snapshot each volume.
	var totalSize int64
	for _, vol := range volumes {
		objectKey := fmt.Sprintf("snapshots/%s/%s/%s.tar.gz", stackID, snapshotID, vol)
		size, err := e.snapshotVolume(ctx, vol, objectKey)
		if err != nil {
			snap.Status = "failed"
			snap.Error = fmt.Sprintf("volume %s: %v", vol, err)
			e.saveSnapshot(ctx, snap) //nolint:errcheck
			return snap, fmt.Errorf("snapshot volume %s: %w", vol, err)
		}
		snap.Volumes = append(snap.Volumes, VolumeSnap{
			Name:      vol,
			ObjectKey: objectKey,
			SizeBytes: size,
		})
		totalSize += size
		e.logger.Infow("volume snapshot uploaded", "volume", vol, "size", size, "key", objectKey)
	}

	snap.SizeBytes = totalSize
	snap.Status = "ready"
	if err := e.saveSnapshot(ctx, snap); err != nil {
		return snap, fmt.Errorf("save snapshot metadata: %w", err)
	}

	e.logger.Infow("snapshot created",
		"stack", stackID,
		"snapshot", snapshotID,
		"volumes", len(snap.Volumes),
		"bytes", totalSize,
	)
	return snap, nil
}

// Restore recreates the named volumes from a snapshot and populates them with the archived data.
// The stack must be stopped before restoring to avoid data corruption.
func (e *Engine) Restore(ctx context.Context, stackID, snapshotID string) error {
	snap, err := e.Get(ctx, stackID, snapshotID)
	if err != nil {
		return fmt.Errorf("snapshot not found: %w", err)
	}
	if snap.Status != "ready" {
		return fmt.Errorf("snapshot %q is not ready (status=%s)", snapshotID, snap.Status)
	}

	for _, vs := range snap.Volumes {
		if err := e.restoreVolume(ctx, vs.Name, vs.ObjectKey); err != nil {
			return fmt.Errorf("restore volume %s: %w", vs.Name, err)
		}
		e.logger.Infow("volume restored", "volume", vs.Name)
	}

	e.logger.Infow("snapshot restored",
		"stack", stackID,
		"snapshot", snapshotID,
		"volumes", len(snap.Volumes),
	)
	return nil
}

// Clone creates a new stack from a snapshot with a different name.
// It copies all volume archives to new volume names and restores the manifest
// so the orchestrator can deploy it as an independent copy.
func (e *Engine) Clone(ctx context.Context, sourceStackID, snapshotID, newStackID string) (*Snapshot, error) {
	snap, err := e.Get(ctx, sourceStackID, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("snapshot not found: %w", err)
	}

	// Load the original manifest and re-name it.
	var desired manifest.Stack
	if err := e.store.GetDesired(ctx, sourceStackID, &desired); err != nil {
		return nil, fmt.Errorf("load source manifest: %w", err)
	}
	desired.Name = newStackID
	desired.Metadata.GitSHA = "" // clear so it's treated as a fresh deploy

	// Copy volume archives to new keys and restore.
	for _, vs := range snap.Volumes {
		// New volume name: replace sourceStackID prefix with newStackID.
		newVolName := strings.Replace(vs.Name, sourceStackID+"-", newStackID+"-", 1)
		newKey := fmt.Sprintf("snapshots/%s/clone/%s/%s.tar.gz", newStackID, snapshotID, newVolName)

		// Copy object within MinIO.
		src := minio.CopySrcOptions{Bucket: e.cfg.Bucket, Object: vs.ObjectKey}
		dst := minio.CopyDestOptions{Bucket: e.cfg.Bucket, Object: newKey}
		if _, err := e.mc.CopyObject(ctx, dst, src); err != nil {
			return nil, fmt.Errorf("copy volume archive: %w", err)
		}

		if err := e.restoreVolume(ctx, newVolName, newKey); err != nil {
			return nil, fmt.Errorf("restore cloned volume %s: %w", newVolName, err)
		}
	}

	// Write the cloned stack desired state to etcd.
	if err := e.store.SetDesired(ctx, newStackID, &desired); err != nil {
		return nil, fmt.Errorf("write cloned stack: %w", err)
	}

	cloned := &Snapshot{
		ID:          uuid.New().String()[:8],
		StackID:     newStackID,
		Label:       fmt.Sprintf("clone of %s@%s", sourceStackID, snapshotID),
		Status:      "ready",
		CreatedAt:   time.Now(),
		ManifestVer: snap.ManifestVer,
	}
	e.saveSnapshot(ctx, cloned) //nolint:errcheck

	e.logger.Infow("stack cloned from snapshot",
		"source", sourceStackID,
		"target", newStackID,
		"snapshot", snapshotID,
	)
	return cloned, nil
}

// List returns all snapshots for a stack (newest first).
func (e *Engine) List(ctx context.Context, stackID string) ([]Snapshot, error) {
	rows, err := e.store.ListSnapshotsRaw(ctx, stackID)
	if err != nil {
		return nil, err
	}
	snaps := make([]Snapshot, 0, len(rows))
	for _, raw := range rows {
		var s Snapshot
		if json.Unmarshal(raw, &s) == nil {
			snaps = append(snaps, s)
		}
	}
	return snaps, nil
}

// Get returns a single snapshot.
func (e *Engine) Get(ctx context.Context, stackID, snapshotID string) (*Snapshot, error) {
	raw, err := e.store.GetSnapshotRaw(ctx, stackID, snapshotID)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Delete removes a snapshot from MinIO and etcd.
func (e *Engine) Delete(ctx context.Context, stackID, snapshotID string) error {
	snap, err := e.Get(ctx, stackID, snapshotID)
	if err != nil {
		return err
	}

	// Remove all objects for this snapshot.
	prefix := fmt.Sprintf("snapshots/%s/%s/", stackID, snapshotID)
	objectCh := e.mc.ListObjects(ctx, e.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	for obj := range objectCh {
		if obj.Err != nil {
			continue
		}
		e.mc.RemoveObject(ctx, e.cfg.Bucket, obj.Key, minio.RemoveObjectOptions{}) //nolint:errcheck
	}

	snap.Status = "deleted"
	e.store.DeleteSnapshotRaw(ctx, stackID, snapshotID) //nolint:errcheck

	e.logger.Infow("snapshot deleted", "stack", stackID, "snapshot", snapshotID)
	return nil
}

// --- internal helpers ---

// snapshotVolume creates a tar.gz of a Docker named volume and uploads it to MinIO.
// Returns the number of bytes uploaded.
func (e *Engine) snapshotVolume(ctx context.Context, volumeName, objectKey string) (int64, error) {
	// Use a temporary alpine container to tar the volume contents.
	// We pipe stdout directly to MinIO via a reader.
	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm",
		"-v", volumeName+":/data:ro",
		"alpine:3", "tar", "czf", "-", "-C", "/data", ".",
	)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start docker tar: %w", err)
	}

	// Upload from the pipe in a goroutine; we don't know the size in advance,
	// so we use -1 (unknown size) which triggers multipart upload in MinIO.
	type result struct {
		info minio.UploadInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := e.mc.PutObject(ctx, e.cfg.Bucket, objectKey, pr, -1,
			minio.PutObjectOptions{
				ContentType: "application/gzip",
				PartSize:    64 * 1024 * 1024, // 64 MiB parts
			},
		)
		pw.Close() //nolint:errcheck
		ch <- result{info, err}
	}()

	if err := cmd.Wait(); err != nil {
		pw.Close() //nolint:errcheck
		return 0, fmt.Errorf("docker tar failed: %w", err)
	}
	pw.Close() //nolint:errcheck

	res := <-ch
	if res.err != nil {
		return 0, fmt.Errorf("upload to minio: %w", res.err)
	}
	return res.info.Size, nil
}

// restoreVolume downloads a volume archive from MinIO and extracts it into a
// (re-)created Docker named volume.
func (e *Engine) restoreVolume(ctx context.Context, volumeName, objectKey string) error {
	// Download object.
	obj, err := e.mc.GetObject(ctx, e.cfg.Bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("get object %s: %w", objectKey, err)
	}
	defer obj.Close()

	// Recreate the Docker volume (remove existing data by removing + recreating).
	exec.CommandContext(ctx, "docker", "volume", "rm", "-f", volumeName).Run() //nolint:errcheck
	if out, err := exec.CommandContext(ctx, "docker", "volume", "create", volumeName).CombinedOutput(); err != nil {
		return fmt.Errorf("create volume %s: %w — %s", volumeName, err, string(out))
	}

	// Use a temporary container to extract the archive into the volume.
	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm", "-i",
		"-v", volumeName+":/data",
		"alpine:3", "tar", "xzf", "-", "-C", "/data",
	)
	cmd.Stdin = obj
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}
	return nil
}

// collectVolumes returns all Docker named-volume names for a stack.
func collectVolumes(stack *manifest.Stack) []string {
	seen := map[string]struct{}{}
	var vols []string
	for _, svc := range stack.Services {
		for _, v := range svc.Volumes {
			name := fmt.Sprintf("%s-%s", stack.Name, v.Name)
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				vols = append(vols, name)
			}
		}
	}
	return vols
}

// saveSnapshot persists snapshot metadata to etcd.
func (e *Engine) saveSnapshot(ctx context.Context, snap *Snapshot) error {
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return e.store.PutSnapshotRaw(ctx, snap.StackID, snap.ID, raw)
}
