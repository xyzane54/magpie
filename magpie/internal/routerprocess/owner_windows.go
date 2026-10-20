package routerprocess

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsOwner struct {
	cmd *exec.Cmd
	mu  sync.Mutex
	job windows.Handle
}

// Starting suspended closes the window in which an abruptly exiting Router
// could leave an unowned model process. Ownership failure prevents execution.
func startOwned(cmd *exec.Cmd) (ownedProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	fail := func(err error) (ownedProcess, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		return fail(fmt.Errorf("assign kill-on-close job: %w", err))
	}
	if err := resumeInitialThread(uint32(cmd.Process.Pid)); err != nil {
		return fail(fmt.Errorf("resume owned process: %w", err))
	}
	return &windowsOwner{cmd: cmd, job: job}, nil
}

// os/exec retains the process handle but closes CreateProcess's initial thread
// handle. Find that still-suspended thread and use documented ResumeThread.
func resumeInitialThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(thread)
		count, err := windows.ResumeThread(thread)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("initial thread suspend count %d, want 1", count)
		}
		return nil
	}
	return fmt.Errorf("initial suspended thread not found: %v", err)
}

func (o *windowsOwner) wait() error {
	err := o.cmd.Wait()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.job != 0 {
		_ = windows.CloseHandle(o.job)
		o.job = 0
	}
	return err
}

func (o *windowsOwner) kill() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.job != 0 {
		_ = windows.TerminateJobObject(o.job, 1)
	}
}
