package routerhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// commitHostGroups also journals model switches. The exact-byte transaction
// keeps the previous connection recoverable until every host file and its
// original-backup journal have been replaced successfully.
func (e Engine) commitHostGroups(ctx context.Context, groups []portGroup, persist func() error) error {
	unlock, err := e.portLocks(groups)
	if err != nil {
		return err
	}
	var marked []string
	locked := true
	release := func() {
		if locked {
			for _, path := range marked {
				_ = os.Remove(path)
			}
			unlock()
			locked = false
		}
	}
	defer release()
	if pending, err := e.readSnapshot(e.StateDir, FilePlan{Name: portTransactionFile}); err != nil || pending.Existed {
		if err != nil {
			return err
		}
		return errors.New("an interrupted host transaction needs recovery before another connection change")
	}
	if err := e.visitPortFiles(groups, func(root string, f snapshot) error {
		now, err := e.readSnapshot(root, f.FilePlan)
		if err != nil || now.Existed != f.Existed || now.Mode != f.Mode || !equal(now.Before, f.Before) {
			return errors.New("host configuration changed during port synchronization planning")
		}
		return nil
	}); err != nil {
		return err
	}
	pending := portTransaction{Version: 1, Groups: groups}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	pending.LockToken = hex.EncodeToString(token[:])
	pending.Checksum = pending.checksum()
	raw, _ := json.Marshal(pending)
	if err := e.writePrivate(hostPath(e.StateDir, portTransactionFile), raw, 0o600); err != nil {
		return err
	}
	verified, err := e.readSnapshot(e.StateDir, FilePlan{Name: portTransactionFile})
	if err != nil || !verified.Existed || !equal(verified.Before, raw) {
		return errors.New("port synchronization backup verification failed; host writes blocked")
	}
	for _, path := range e.portLockPaths(groups) {
		marker := filepath.Join(path, "port-sync-owner")
		if err := e.writePrivate(marker, []byte(pending.LockToken), 0o600); err != nil {
			release()
			_ = os.Remove(hostPath(e.StateDir, portTransactionFile))
			return err
		}
		marked = append(marked, marker)
	}
	applyErr := e.visitPortFiles(groups, func(root string, f snapshot) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if equal(f.Before, f.After) {
			return nil
		}
		return e.replace(root, f)
	})
	if applyErr == nil {
		applyErr = persist()
	}
	if applyErr != nil {
		rollbackErr := e.rollbackPortFiles(groups)
		if rollbackErr == nil {
			release()
			rollbackErr = os.Remove(hostPath(e.StateDir, portTransactionFile))
		}
		return errors.Join(applyErr, rollbackErr)
	}
	pending.Committed = true
	pending.Checksum = pending.checksum()
	raw, _ = json.Marshal(pending)
	if err := e.writePrivate(hostPath(e.StateDir, portTransactionFile), raw, 0o600); err != nil {
		return err
	}
	release()
	return os.Remove(hostPath(e.StateDir, portTransactionFile))
}

// RecoverPortSync restores only bytes written by an interrupted host transaction.
// An unknown operation lock or a later user edit blocks automatic recovery.
func RecoverPortSync(ctx context.Context, stateDir string) error {
	e := Engine{StateDir: stateDir}
	if err := validateRoot(stateDir); err != nil {
		return err
	}
	s, err := e.readSnapshot(stateDir, FilePlan{Name: portTransactionFile})
	if err != nil || !s.Existed {
		return err
	}
	var pending portTransaction
	if len(s.Before) > 16<<20 || json.Unmarshal(s.Before, &pending) != nil || pending.Version != 1 ||
		len(pending.Groups) == 0 || len(pending.Groups) > 64 || len(pending.LockToken) != 32 || pending.Checksum != pending.checksum() {
		return errors.New("port synchronization recovery record is invalid")
	}
	for _, group := range pending.Groups {
		a, err := portAdapter(group.Host)
		if err != nil {
			return err
		}
		if err := e.validateState(group.Root); err != nil {
			return err
		}
		contract := a.Contract()
		if len(group.Files) != len(contract.Files) || group.Journal.Name != filepath.Base(e.recordPath(group.Host, group.Root)) || group.Journal.Mode&^0o777 != 0 {
			return errors.New("invalid port recovery file set")
		}
		for i, f := range group.Files {
			if f.Name != contract.Files[i].Name || f.Mode&^0o777 != 0 {
				return errors.New("invalid port recovery file metadata")
			}
		}
	}
	if err := e.recoverPortLocks(pending); err != nil {
		return err
	}
	if pending.Committed {
		return os.Remove(hostPath(stateDir, portTransactionFile))
	}
	unlock, err := e.portLocks(pending.Groups)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.rollbackPortFiles(pending.Groups); err != nil {
		return err
	}
	return os.Remove(hostPath(stateDir, portTransactionFile))
}
