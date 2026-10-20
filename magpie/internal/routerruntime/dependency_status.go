package routerruntime

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/yetone/magpie/internal/routerdownload"
	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/routerprocess"
)

func dependencyCode(err error) string {
	var d *routerprocess.DependencyError
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

func operationCode(err error, fallback string) string {
	if code := dependencyCode(err); code != "" {
		return code
	}
	return fallback
}

func classifyInstallError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, routerdownload.ErrDiskSpace) {
		return "disk_space"
	}
	if errors.Is(err, routerdownload.ErrIntegrity) {
		return "integrity"
	}
	if errors.Is(err, routerdownload.ErrBusy) {
		return "busy"
	}
	switch dependencyCode(err) {
	case routerprocess.DependencyCorruptCode:
		return "integrity"
	case routerprocess.DependencyArchitectureCode:
		return "architecture"
	case routerprocess.DependencyVersionCode:
		return "version"
	case routerprocess.DependencyLayoutCode:
		return "layout"
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "stalled waiting for response headers"),
		strings.Contains(msg, "timeout awaiting response headers"),
		strings.Contains(msg, "tls handshake timeout"),
		strings.Contains(msg, "download stalled after"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "download http status"),
		strings.Contains(msg, "unexpected partial response"),
		strings.Contains(msg, "resource changed while resuming"),
		strings.Contains(msg, "encoded artifact response refused"):
		return "http_status"
	case strings.Contains(msg, "download body:"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "unexpected eof"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "wsarecv"),
		strings.Contains(msg, "wsasend"),
		strings.Contains(msg, "dial tcp"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "connectex"):
		return "interrupted"
	}
	return "failed"
}

func installErrorCode(stage, reason string) string {
	if stage == "" {
		return "installation_failed"
	}
	if reason == "" {
		reason = "failed"
	}
	return "local_model_install_" + stage + "_" + reason
}

func (m *Manager) updateDependenciesLocked(r routermanifest.Runtime, err error) {
	if r.Dependencies == nil {
		m.dependencies = DependencyStatus{State: "not_required"}
		return
	}
	d := DependencyStatus{State: "unknown", ID: r.Dependencies.ID, Version: r.Dependencies.Version, Recovery: "install_or_repair"}
	if err == nil {
		d.State = "ready"
		d.Recovery = ""
	} else if code := dependencyCode(err); code != "" {
		d.State = strings.TrimPrefix(code, "local_model_dependency_")
		if d.State == "download_failed" {
			d.State = "failed"
		}
		d.Error = code
	} else if errors.Is(err, context.Canceled) {
		d.State = "canceled"
	}
	m.dependencies = d
}
