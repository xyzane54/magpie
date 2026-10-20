package routerdevice

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// COM method order and layouts follow dxgi.h/dxgi1_2.h and d3d12.h. DXGI's
// dedicated segment alone does not prove discrete VRAM: UMA can reserve one.
type comObject struct{ vtable *[44]uintptr }

//go:uintptrescapes
func (p *comObject) call(method int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(p.vtable[method], append([]uintptr{uintptr(unsafe.Pointer(p))}, args...)...)
	runtime.KeepAlive(p)
	return r
}

func (p *comObject) release()       { p.call(2) }
func hresultFailed(hr uintptr) bool { return int32(hr) < 0 }

type adapterDescription struct {
	Name                                                            [128]uint16
	VendorID, DeviceID, SubSystemID, Revision                       uint32
	DedicatedVideoMemory, DedicatedSystemMemory, SharedSystemMemory uintptr
	LUIDLow                                                         uint32
	LUIDHigh                                                        int32
	Flags                                                           uint32
}

func windowsAdapters(ctx context.Context) ([]Adapter, error) {
	create := windows.NewLazySystemDLL("dxgi.dll").NewProc("CreateDXGIFactory1")
	if err := create.Find(); err != nil {
		return nil, err
	}
	iid := windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	var factory *comObject
	hr, _, _ := create.Call(uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&factory)))
	if hresultFailed(hr) || factory == nil {
		return nil, fmt.Errorf("DXGI factory: HRESULT %#x", uint32(hr))
	}
	defer factory.release()
	adapters := []Adapter{}
	for i := uintptr(0); i < 256; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var adapter *comObject
		hr = factory.call(12, i, uintptr(unsafe.Pointer(&adapter)))
		if uint32(hr) == 0x887a0002 {
			return adapters, nil
		} // DXGI_ERROR_NOT_FOUND.
		if hresultFailed(hr) || adapter == nil {
			return nil, fmt.Errorf("DXGI enumerate: HRESULT %#x", uint32(hr))
		}
		a, err := describeAdapter(adapter)
		adapter.release()
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, a)
	}
	return nil, fmt.Errorf("DXGI adapter count exceeded limit")
}

func describeAdapter(adapter *comObject) (Adapter, error) {
	var desc adapterDescription
	hr := adapter.call(10, uintptr(unsafe.Pointer(&desc)))
	if hresultFailed(hr) {
		return Adapter{}, fmt.Errorf("DXGI description: HRESULT %#x", uint32(hr))
	}
	a := Adapter{ID: fmt.Sprintf("%08x:%08x", uint32(desc.LUIDHigh), desc.LUIDLow), Name: windows.UTF16ToString(desc.Name[:]), Discrete: UnknownAvailability, MemoryBytes: Known(uint64(desc.DedicatedVideoMemory))}
	if desc.Flags&3 != 0 {
		a.Discrete = Unavailable
		return a, nil
	} // Remote or software.
	uma := adapterUMA(adapter)
	if uma.Value != nil {
		a.Discrete = Available
		if *uma.Value {
			a.Discrete = Unavailable
		}
	}
	return a, nil
}

func adapterUMA(adapter *comObject) Fact[bool] {
	create := windows.NewLazySystemDLL("d3d12.dll").NewProc("D3D12CreateDevice")
	if err := create.Find(); err != nil {
		return Unknown[bool](err.Error())
	}
	iid := windows.GUID{Data1: 0x189819f1, Data2: 0x1db6, Data3: 0x4b57, Data4: [8]byte{0xbe, 0x54, 0x18, 0x21, 0x33, 0x9b, 0x85, 0xf7}}
	var device *comObject
	hr, _, _ := create.Call(uintptr(unsafe.Pointer(adapter)), 0xb000, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&device)))
	runtime.KeepAlive(adapter)
	if hresultFailed(hr) || device == nil {
		return Unknown[bool](fmt.Sprintf("D3D12 architecture device: HRESULT %#x", uint32(hr)))
	}
	defer device.release()
	var architecture struct {
		NodeIndex                                uint32
		TileBasedRenderer, UMA, CacheCoherentUMA int32
	}
	hr = device.call(13, 1, uintptr(unsafe.Pointer(&architecture)), unsafe.Sizeof(architecture))
	if hresultFailed(hr) {
		return Unknown[bool](fmt.Sprintf("D3D12 architecture: HRESULT %#x", uint32(hr)))
	}
	return Known(architecture.UMA != 0)
}
