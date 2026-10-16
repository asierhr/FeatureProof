package localvalidation

import (
	"os"
	"syscall"
)

func singleLink(file *os.File) bool {
	var data syscall.ByHandleFileInformation
	return syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &data) == nil && data.NumberOfLinks == 1
}
