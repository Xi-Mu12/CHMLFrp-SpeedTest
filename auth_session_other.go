//go:build !windows

package main

// 非 Windows 平台没有 DPAPI，退化为明文存储。
// 文件权限已经由 sessionFilePath 的目录（0700）与文件的 0600 兜住。

func sealBytes(plain []byte) ([]byte, error) {
	return plain, nil
}

func openBytes(sealed []byte) ([]byte, error) {
	return sealed, nil
}
