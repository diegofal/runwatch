// Package sysusage reads how busy this PC is: processor time, memory, and
// every process with its parent and the processor time it has used. It
// needs no admin rights and opens no process: one system call lists them
// all, including the runners' (which run as a service account).
package sysusage

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"runwatch/internal/domain"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// ticks turns a count of 100-nanosecond units into a Duration.
func ticks(n int64) time.Duration { return time.Duration(n) * 100 }

func filetime(ft windows.Filetime) int64 {
	return int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
}

// created turns a process's start time, as Windows counts it, into a Time.
func created(v int64) time.Time {
	ft := windows.Filetime{LowDateTime: uint32(v), HighDateTime: uint32(v >> 32)}
	return time.Unix(0, ft.Nanoseconds())
}

// Read returns the PC's usage at this moment.
func Read(context.Context) (domain.Reading, error) {
	r := domain.Reading{At: time.Now(), Cores: int(windows.GetActiveProcessorCount(windows.ALL_PROCESSOR_GROUPS))}

	var idle, kernel, user windows.Filetime
	if ok, _, err := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok == 0 {
		return r, fmt.Errorf("read the processor times: %w", err)
	}
	// The kernel time Windows reports includes the idle time.
	r.Busy = ticks(filetime(kernel) + filetime(user) - filetime(idle))

	mem := memoryStatusEx{}
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem))); ok == 0 {
		return r, fmt.Errorf("read the memory in use: %w", err)
	}
	r.MemTotal, r.MemUsed = int64(mem.TotalPhys), int64(mem.TotalPhys-mem.AvailPhys)

	var err error
	if r.Procs, err = processes(); err != nil {
		return r, err
	}
	return r, nil
}

// listSize is the buffer size the last listing needed: asking twice costs
// twice, and the list is read several times a second.
var listSize atomic.Uint32

// processes lists every process. The list has no fixed size, so the call is
// repeated with the size Windows asks for until it fits.
func processes() ([]domain.Proc, error) {
	size := max(listSize.Load(), 1<<20)
	for range 8 {
		buf := make([]byte, size)
		var needed uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &needed)
		if errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) {
			size = max(needed, size) + 1<<18
			listSize.Store(size)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list the processes: %w", err)
		}
		return parse(buf[:min(needed, size)]), nil
	}
	return nil, errors.New("list the processes: the list kept growing")
}

// parse walks the records Windows wrote into buf; each says where the next starts.
func parse(buf []byte) []domain.Proc {
	var out []domain.Proc
	record := int(unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{}))
	for off := 0; off+record <= len(buf); {
		p := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
		out = append(out, domain.Proc{
			PID:     int(p.UniqueProcessID),
			Parent:  int(p.InheritedFromUniqueProcessID),
			Created: created(p.CreateTime),
			CPU:     ticks(p.UserTime + p.KernelTime),
			Mem:     int64(p.WorkingSetSize),
		})
		if p.NextEntryOffset == 0 {
			break
		}
		off += int(p.NextEntryOffset)
	}
	return out
}
