package routercontrol

import (
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/testenv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { testenv.Offline(); testenv.Main(m) }
func TestProcessIdentityRejectsPIDReuseAndChangedExecutable(t *testing.T) {
	stamp, exe, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	r := Record{PID: os.Getpid(), Executable: exe, ProcessStamp: stamp}
	if err := Verify(r); err != nil {
		t.Fatal(err)
	}
	r.ProcessStamp += "reused"
	if Verify(r) == nil {
		t.Fatal("reused PID accepted")
	}
	r.ProcessStamp = stamp
	r.Executable = filepath.Join(t.TempDir(), "other.exe")
	if Verify(r) == nil {
		t.Fatal("foreign executable accepted")
	}
}

func TestControlAddressCannotSendManagementTokenElsewhere(t *testing.T) {
	l := dirs.Layout{State: t.TempDir()}
	if err := os.Mkdir(l.ServiceLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.ServiceLock(), "management-token"), []byte("private-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp, exe, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"example.test:80", "192.0.2.1:80", "127.0.0.1:0", "127.0.0.1:123/a", "127.0.0.1:80@evil.test"} {
		r := Record{PID: os.Getpid(), Executable: exe, ProcessStamp: stamp, Address: address}
		if _, err := request(context.Background(), l, r, ""); err == nil {
			t.Fatal("unsafe address accepted", address)
		}
	}
	var authenticated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authenticated = true
		}
		w.Write([]byte(`{"service":"another-local-program","status":"ok"}`))
	}))
	defer server.Close()
	r := Record{PID: os.Getpid(), Executable: exe, ProcessStamp: stamp, Address: strings.TrimPrefix(server.URL, "http://")}
	if _, err := request(context.Background(), l, r, ""); err == nil || authenticated {
		t.Fatal("management token sent to an unrelated listener", err, authenticated)
	}
}
func TestSavedTrialOptionsAndBrokenStateFailClosed(t *testing.T) {
	l := dirs.Layout{State: t.TempDir()}
	if got, err := preference(l); err != nil || got {
		t.Fatal(got, err)
	}
	if err := SaveOptions(l, true); err != nil {
		t.Fatal(err)
	}
	if got, err := preference(l); err != nil || !got {
		t.Fatal("explicit trial lost", got, err)
	}
	if err := os.WriteFile(filepath.Join(l.State, "service-options.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := preference(l); err == nil {
		t.Fatal("damaged trial choice treated as default")
	}
}
func TestStatusDoesNotAdoptUnknownLockOrForeignExecutable(t *testing.T) {
	l := dirs.Layout{State: t.TempDir()}
	if err := os.Mkdir(l.ServiceLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Current(context.Background(), l); err == nil {
		t.Fatal("unknown lock reported stopped")
	}
	stamp, _, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	r := Record{PID: os.Getpid(), ProcessStamp: stamp, InstanceID: "fixture", Executable: filepath.Join(t.TempDir(), "foreign.exe")}
	b, _ := json.Marshal(r)
	if err = os.WriteFile(RecordFile(l), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stop(context.Background(), l); err == nil {
		t.Fatal("foreign executable controlled")
	}
	if _, err := os.Stat(RecordFile(l)); err != nil {
		t.Fatal("foreign record changed", err)
	}
}
