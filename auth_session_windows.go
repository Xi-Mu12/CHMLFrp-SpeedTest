//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 平台用 DPAPI（CryptProtectData / CryptUnprotectData）加解密登录态。
// 密钥由系统按当前用户派生，文件被拷走也解不开；不需要自己管密钥。

var (
	crypt32DLL             = windows.NewLazySystemDLL("crypt32.dll")
	kernel32DLL            = windows.NewLazySystemDLL("kernel32.dll")
	procCryptProtectData   = crypt32DLL.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32DLL.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32DLL.NewProc("LocalFree")
)

// CRYPTPROTECT_UI_FORBIDDEN：加密/解密过程中不弹任何系统 UI。
const cryptProtectUIForbidden = 0x1

// dataBlob 对应 Win32 的 DATA_BLOB。
type dataBlob struct {
	size uint32
	data *byte
}

func makeBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{size: uint32(len(b)), data: &b[0]}
}

// bytes 复制出 Go 侧切片（LocalAlloc 的内存随后要 LocalFree，不能直接引用）。
func (b dataBlob) bytes() []byte {
	if b.data == nil || b.size == 0 {
		return nil
	}
	out := make([]byte, b.size)
	copy(out, unsafe.Slice(b.data, int(b.size)))
	return out
}

func (b dataBlob) free() {
	if b.data != nil {
		_, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(b.data)))
	}
}

// sealBytes 用 DPAPI 加密。
func sealBytes(plain []byte) ([]byte, error) {
	in := makeBlob(plain)
	var out dataBlob
	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), // pDataIn
		0, // szDataDescr
		0, // pOptionalEntropy
		0, // pvReserved
		0, // pPromptStruct
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)), // pDataOut
	)
	if r == 0 {
		return nil, err
	}
	defer out.free()
	return out.bytes(), nil
}

// openBytes 用 DPAPI 解密。
func openBytes(sealed []byte) ([]byte, error) {
	in := makeBlob(sealed)
	var out dataBlob
	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, err
	}
	defer out.free()
	return out.bytes(), nil
}
