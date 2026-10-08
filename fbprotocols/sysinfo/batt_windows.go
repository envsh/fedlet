//go:build windows

// batt_windows.go — battery reading through the SetupAPI / Battery IOCTLs.
//
// Windows exposes batteries as device interfaces under GUID_DEVCLASS_BATTERY.
// The enumeration and the two-ioctl sequence below follow distatus/battery,
// whose MIT licence and copyright are reproduced verbatim underneath. The one
// substantive difference is that this port reads BATTERY_INFORMATION.CycleCount
// — the struct field upstream defines but never copies out — which is the only
// place Windows reports charge cycles, and it reads BATTERY_QUERY_STATUS for
// the charge rate and on-line state.
//
// Units per the Windows driver model:
//
//	DesignedCapacity / FullChargedCapacity / Capacity → mWh (or a relative
//	                                                    0..100 value when the
//	                                                    BATTERY_CAPACITY_RELATIVE
//	                                                    capability is set)
//	Voltage → mV → V;  Rate → mW (signed, 0x80000000 = unknown)
//	CycleCount → cycles (0xFFFFFFFF = not reported)
//
// This file cannot be exercised on the Linux development host; it is verified
// by GOOS=windows cross-compilation.
package sysinfo

import (
	"errors"
	"fmt"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// battery
// Copyright (C) 2016-2017,2023 Karol 'Kenji Takahashi' Woźniak
//
// Permission is hereby granted, free of charge, to any person obtaining
// a copy of this software and associated documentation files (the "Software"),
// to deal in the Software without restriction, including without limitation
// the rights to use, copy, modify, merge, publish, distribute, sublicense,
// and/or sell copies of the Software, and to permit persons to whom
// the Software is furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included
// in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
// THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
// FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
// DEALINGS IN THE SOFTWARE.

// IOCTL codes and constants from the Windows SDK.
const (
	ioctlBatteryQueryTag         = 2703424 // IOCTL_BATTERY_QUERY_TAG
	ioctlBatteryQueryInformation = 2703428 // IOCTL_BATTERY_QUERY_INFORMATION
	ioctlBatteryQueryStatus      = 2703436 // IOCTL_BATTERY_QUERY_STATUS

	digcfPresent         = 0x00000002
	digcfDeviceInterface = 0x00000010

	batteryCharging         = 0x00000004
	batteryDischarging      = 0x00000002
	batteryPowerOnLine      = 0x00000001
	batteryCapacityRelative = 0x10000000

	batteryUnknownRate      = -0x80000000
	batteryUnknownCapacity  = 0xffffffff
	batteryInformationLevel = 0 // BatteryInformation
)

// errNoMoreItems ends the enumeration loop; it is a normal stop, not a fault.
var errNoMoreItems = errors.New("no more batteries")

// batteryQueryInformation is the IOCTL_BATTERY_QUERY_INFORMATION input.
type batteryQueryInformation struct {
	BatteryTag       uint32
	InformationLevel int32
	AtRate           int32
}

// batteryInformation mirrors BATTERY_INFORMATION, including the CycleCount
// field upstream leaves unread.
type batteryInformation struct {
	Capabilities        uint32
	Technology          uint8
	Reserved            [3]uint8
	Chemistry           [4]uint8
	DesignedCapacity    uint32
	FullChargedCapacity uint32
	DefaultAlert1       uint32
	DefaultAlert2       uint32
	CriticalBias        uint32
	CycleCount          uint32
}

// batteryStatus mirrors BATTERY_STATUS.
type batteryStatus struct {
	PowerState uint32
	Capacity   uint32
	Voltage    uint32
	Rate       int32
}

// spDeviceInterfaceData mirrors SP_DEVICE_INTERFACE_DATA; Reserved is a
// ULONG_PTR, hence Go's platform-sized uint.
type spDeviceInterfaceData struct {
	cbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uint
}

// guidDeviceBattery is GUID_DEVCLASS_BATTERY.
var guidDeviceBattery = windows.GUID{
	Data1: 0x72631e54,
	Data2: 0x78A4,
	Data3: 0x11d0,
	Data4: [8]byte{0xbc, 0xf7, 0x00, 0xaa, 0x00, 0xb7, 0xb3, 0x2a},
}

var setupapi = windows.NewLazySystemDLL("setupapi.dll")

var (
	procSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

// readBatteries enumerates every present battery interface.
func readBatteries(errs *errCollector) ([]BatteryInfo, error) {
	hdev, _, err := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guidDeviceBattery)),
		0,
		0,
		uintptr(digcfPresent|digcfDeviceInterface),
	)
	if hdev == ^uintptr(0) || hdev == 0 {
		// INVALID_HANDLE_VALUE is all-bits-one.
		if err != nil {
			return nil, fmt.Errorf("SetupDiGetClassDevs: %w", err)
		}
		return nil, errors.New("SetupDiGetClassDevs returned no handle")
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hdev)

	var bats []BatteryInfo
	for i := 0; i < 32; i++ {
		b, err := readBatteryAt(hdev, uint32(i))
		if errors.Is(err, errNoMoreItems) {
			break
		}
		if err != nil {
			// Rule D: one unusable interface does not hide the others.
			errs.Add(fmt.Sprintf("battery:%d", i), err.Error())
			continue
		}
		bats = append(bats, b)
	}
	return bats, nil
}

// readBatteryAt opens the idx-th battery interface and issues the three
// queries that make up a reading.
func readBatteryAt(hdev uintptr, idx uint32) (BatteryInfo, error) {
	b := invalidBattery()

	var did spDeviceInterfaceData
	did.cbSize = uint32(unsafe.Sizeof(did))
	r1, _, callErr := procSetupDiEnumDeviceInterfaces.Call(
		hdev, 0,
		uintptr(unsafe.Pointer(&guidDeviceBattery)),
		uintptr(idx),
		uintptr(unsafe.Pointer(&did)),
	)
	if r1 == 0 {
		if callErr == windows.ERROR_NO_MORE_ITEMS {
			return BatteryInfo{}, errNoMoreItems
		}
		return BatteryInfo{}, fmt.Errorf("SetupDiEnumDeviceInterfaces: %w", callErr)
	}

	// The detail struct embeds a trailing ANYSIZE_ARRAY of UTF-16, so it has
	// to be sized in two passes and its CbSize field is 8 on 64-bit but 6 on
	// 32-bit Windows.
	var cbRequired uint32
	procSetupDiGetDeviceInterfaceDetailW.Call(
		hdev,
		uintptr(unsafe.Pointer(&did)),
		0, 0,
		uintptr(unsafe.Pointer(&cbRequired)),
		0,
	)
	if cbRequired < 4 {
		return BatteryInfo{}, errors.New("SetupDiGetDeviceInterfaceDetail returned no size")
	}

	didd := make([]uint16, cbRequired/2)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		*(*uint32)(unsafe.Pointer(&didd[0])) = 8
	} else {
		*(*uint32)(unsafe.Pointer(&didd[0])) = 6
	}
	r1, _, callErr = procSetupDiGetDeviceInterfaceDetailW.Call(
		hdev,
		uintptr(unsafe.Pointer(&did)),
		uintptr(unsafe.Pointer(&didd[0])),
		uintptr(cbRequired),
		uintptr(unsafe.Pointer(&cbRequired)),
		0,
	)
	if r1 == 0 {
		return BatteryInfo{}, fmt.Errorf("SetupDiGetDeviceInterfaceDetail: %w", callErr)
	}

	handle, err := windows.CreateFile(
		&didd[2],
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return BatteryInfo{}, fmt.Errorf("CreateFile battery: %w", err)
	}
	defer windows.CloseHandle(handle)

	var dwOut uint32
	var tag uint32
	if err := windows.DeviceIoControl(
		handle, ioctlBatteryQueryTag,
		(*byte)(unsafe.Pointer(new(uint32))), uint32(unsafe.Sizeof(uint32(0))),
		(*byte)(unsafe.Pointer(&tag)), uint32(unsafe.Sizeof(tag)),
		&dwOut, nil,
	); err != nil {
		return BatteryInfo{}, fmt.Errorf("IOCTL_BATTERY_QUERY_TAG: %w", err)
	}
	if tag == 0 {
		return BatteryInfo{}, errors.New("battery tag was zero")
	}

	bqi := batteryQueryInformation{BatteryTag: tag, InformationLevel: batteryInformationLevel}
	var bi batteryInformation
	if err := windows.DeviceIoControl(
		handle, ioctlBatteryQueryInformation,
		(*byte)(unsafe.Pointer(&bqi)), uint32(unsafe.Sizeof(bqi)),
		(*byte)(unsafe.Pointer(&bi)), uint32(unsafe.Sizeof(bi)),
		&dwOut, nil,
	); err != nil {
		return BatteryInfo{}, fmt.Errorf("IOCTL_BATTERY_QUERY_INFORMATION: %w", err)
	}

	var bs batteryStatus
	if err := windows.DeviceIoControl(
		handle, ioctlBatteryQueryStatus,
		(*byte)(unsafe.Pointer(&batteryStatusQuery{BatteryTag: tag})), uint32(unsafe.Sizeof(uint32(0))),
		(*byte)(unsafe.Pointer(&bs)), uint32(unsafe.Sizeof(bs)),
		&dwOut, nil,
	); err != nil {
		return BatteryInfo{}, fmt.Errorf("IOCTL_BATTERY_QUERY_STATUS: %w", err)
	}

	applyWindowsInfo(&b, bi)
	applyWindowsStatus(&b, bs)
	return b, nil
}

// batteryStatusQuery is the IOCTL_BATTERY_QUERY_STATUS input (a battery tag
// followed by an ignored timeout).
type batteryStatusQuery struct {
	BatteryTag uint32
	Timeout    uint32
}

// applyWindowsInfo copies BATTERY_INFORMATION fields, honouring the relative
// capacity capability which turns absolute mWh into an opaque 0..100 scale.
func applyWindowsInfo(b *BatteryInfo, bi batteryInformation) {
	relative := bi.Capabilities&batteryCapacityRelative != 0

	if bi.DesignedCapacity != batteryUnknownCapacity && !relative {
		b.Design = float64(bi.DesignedCapacity)
	}
	if bi.FullChargedCapacity != batteryUnknownCapacity && !relative {
		b.Full = float64(bi.FullChargedCapacity)
	}
	if bi.CycleCount != batteryUnknownCapacity && bi.CycleCount != 0 {
		b.Cycles = float64(bi.CycleCount)
	}
	b.HealthPercent = healthPercent(b.Full, b.Design)
}

// applyWindowsStatus copies BATTERY_STATUS: charge level, voltage, signed rate
// and the power-state flags that give charging/on-line state.
func applyWindowsStatus(b *BatteryInfo, bs batteryStatus) {
	if bs.Capacity != batteryUnknownCapacity {
		if b.Full > 0 {
			b.Current = float64(bs.Capacity)
			b.Percent = guardPct(b.Current, b.Full)
		} else {
			// Relative capacity: the driver reports a plain percentage.
			p := float64(bs.Capacity)
			if p >= 0 && p <= 100 {
				b.Current = p
				b.Percent = p
			} else {
				b.Current = InvalidNum
				b.Percent = InvalidNum
			}
		}
	}
	if bs.Voltage != 0 {
		b.Voltage = float64(bs.Voltage) / 1000 // mV → V
	}
	if int32(bs.Rate) == batteryUnknownRate {
		b.ChargeRate = InvalidNum
	} else {
		b.ChargeRate = guardNum(math.Abs(float64(int32(bs.Rate))))
		if bs.PowerState&batteryDischarging != 0 {
			b.ChargeRate = -b.ChargeRate
		}
	}

	charging := bs.PowerState&batteryCharging != 0
	discharging := bs.PowerState&batteryDischarging != 0
	onLine := bs.PowerState&batteryPowerOnLine != 0

	b.Present = TriYes
	b.Charging = tri(true, charging)
	switch {
	case charging:
		b.State = "Charging"
		b.ACPower = TriYes
	case discharging:
		b.State = "Discharging"
		b.ACPower = TriNo
	default:
		b.State = "Idle"
		b.ACPower = tri(true, onLine)
	}
}
