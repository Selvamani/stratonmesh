package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

// ResolvePortVarRefs fills in HostRange / Host on every PortSpec whose VarRef
// points to an entry in stack.PortVars.  Call this once after parsing.
//
// portVar values:
//   - "3000-3010"  → HostRange is set to that range string; Host left 0
//   - "8080"       → Host is set to 8080; HostRange cleared
//   - "0" or ""    → Host stays 0 (auto-assign near Expose at allocation time)
func ResolvePortVarRefs(stack *Stack) error {
	for i := range stack.Services {
		svc := &stack.Services[i]
		for j := range svc.Ports {
			p := &svc.Ports[j]
			if p.VarRef == "" {
				continue
			}
			val, ok := stack.PortVars[p.VarRef]
			if !ok {
				return fmt.Errorf("service %q port expose=%d: portVar %q not declared in portVars",
					svc.Name, p.Expose, p.VarRef)
			}
			val = strings.TrimSpace(val)
			if strings.Contains(val, "-") {
				// Range spec
				p.HostRange = val
				p.Host = 0
			} else if val == "" || val == "0" {
				// Auto-assign near expose port
				p.Host = 0
				p.HostRange = ""
			} else {
				n, err := strconv.Atoi(val)
				if err != nil {
					return fmt.Errorf("portVar %q: invalid value %q (expected range, int, or 0)", p.VarRef, val)
				}
				p.Host = n
				p.HostRange = ""
			}
		}
	}
	return nil
}

// AllocatePorts allocates concrete host ports for every PortSpec in the stack
// and returns a map of varName → allocated port.  The same portVar referenced
// by multiple services is allocated only once; subsequent services reuse the
// same port.
//
// alloc(start, end int) is called for each port that needs allocation:
//   - range allocation: start=rangeStart, end=rangeEnd  (end > start)
//   - preferred/auto:   start=preferred,   end=-1
//
// Adapters should pass portalloc.FindInRange / FindAvailable wrapped in this
// signature.  After this call every PortSpec with a VarRef has Host set to the
// concrete port and HostRange cleared.
func AllocatePorts(stack *Stack, alloc func(start, end int) (int, error)) (map[string]int, error) {
	resolved := make(map[string]int)
	for i := range stack.Services {
		svc := &stack.Services[i]
		for j := range svc.Ports {
			p := &svc.Ports[j]
			if p.VarRef == "" {
				continue
			}
			// Reuse already-allocated port for this variable.
			if port, ok := resolved[p.VarRef]; ok {
				p.Host = port
				p.HostRange = ""
				continue
			}
			var (
				port int
				err  error
			)
			if p.HostRange != "" && strings.Contains(p.HostRange, "-") {
				parts := strings.SplitN(p.HostRange, "-", 2)
				start, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
				end, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
				if e1 != nil || e2 != nil || end < start {
					return nil, fmt.Errorf("portVar %q: invalid range %q", p.VarRef, p.HostRange)
				}
				port, err = alloc(start, end)
			} else {
				preferred := p.Host
				if preferred == 0 {
					preferred = p.Expose // fall back to container port as hint
				}
				port, err = alloc(preferred, -1)
			}
			if err != nil {
				return nil, fmt.Errorf("allocate portVar %q: %w", p.VarRef, err)
			}
			resolved[p.VarRef] = port
			p.Host = port
			p.HostRange = ""
		}
	}
	return resolved, nil
}

// InjectResolvedPorts substitutes $varname and ${varname} references in every
// service's env var values with the concrete allocated port from resolved.
// Call this after AllocatePorts so env vars carry the actual port number.
func InjectResolvedPorts(stack *Stack, resolved map[string]int) {
	if len(resolved) == 0 {
		return
	}
	for i := range stack.Services {
		svc := &stack.Services[i]
		for k, v := range svc.Env {
			for varName, port := range resolved {
				portStr := strconv.Itoa(port)
				v = strings.ReplaceAll(v, "${"+varName+"}", portStr)
				v = strings.ReplaceAll(v, "$"+varName, portStr)
			}
			svc.Env[k] = v
		}
	}
}
