//go:build !windows

package routerdelivery

import "os"

func checkPlatformName(string) error        { return nil }
func checkPlatformPath(string) error        { return nil }
func checkFile(*os.File, os.FileInfo) error { return nil }
