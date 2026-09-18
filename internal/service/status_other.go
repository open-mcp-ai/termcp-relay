//go:build !windows

package service

import (
	"errors"
	"strings"

	kservice "github.com/kardianos/service"
)

// queryService inspects the host service manager through kardianos/service,
// which shells out to systemctl / launchctl. Neither needs privileges for a
// read-only query.
func queryService(name string) (Status, error) {
	st := Status{Name: name}

	s, err := kservice.New(&Program{}, &kservice.Config{Name: name})
	if err != nil {
		return st, err
	}
	svcStatus, err := s.Status()
	switch {
	case errors.Is(err, kservice.ErrNotInstalled):
		return st, nil
	case err != nil:
		// systemd reports a failed unit as an error; that still means the
		// service is installed, just not healthy.
		if strings.Contains(err.Error(), "failed state") {
			st.Installed = true
			st.Detail = "unit is in a failed state; inspect with: systemctl status " + name
			return st, nil
		}
		return st, err
	}
	st.Installed = true
	st.Running = svcStatus == kservice.StatusRunning
	return st, nil
}
