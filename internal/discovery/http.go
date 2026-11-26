package discovery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Handler serves the HTTP SD response. Query parameters:
//
//   - role: vm (default), gameserver or dns. It must be enabled with --discovery.role.
//   - port: for vm, overrides the configured target port; for gameserver,
//     replaces each server's own port; for dns, is appended to each host name.
//
// Until the role's first refresh succeeds it answers 503, so Prometheus keeps
// its current targets instead of dropping them. Afterwards it always answers
// 200 with the latest list ([] when there is nothing), as the HTTP SD spec requires.
func (d *Discoverer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		role := strings.ToLower(strings.TrimSpace(q.Get("role")))
		if role == "" {
			role = RoleVM
		}
		if !isRole(role) {
			http.Error(w, fmt.Sprintf("unknown role %q: use %s", role, strings.Join(AllRoles, ", ")), http.StatusBadRequest)
			return
		}
		if !d.Enabled(role) {
			http.Error(w, fmt.Sprintf("role %q is not enabled: start the service with --discovery.role=%s", role, role), http.StatusBadRequest)
			return
		}
		port := 0
		if v := q.Get("port"); v != "" {
			p, err := ParsePort(v)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			port = p
		}
		if !d.Ready(role) {
			http.Error(w, "the "+role+" list has not been fetched from the NodeRings API yet", http.StatusServiceUnavailable)
			return
		}
		body, err := json.Marshal(d.TargetGroups(role, port))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(append(body, '\n'))
	})
}
