package util

import (
	"fmt"
	"testing"
)

func TestCheckDeviceType(t *testing.T) {
	annos := make(map[string]string)
	annos[PodAnnotationUseGpuType] = "A100,H100"
	annos[PodAnnotationUnUseGpuType] = "4090,4080,H100"
	matched := CheckDeviceType(annos, "A100")
	fmt.Println("check result:", matched)
	matched = CheckDeviceType(annos, "4090")
	fmt.Println("check result:", matched)
	matched = CheckDeviceType(annos, "3080")
	fmt.Println("check result:", matched)
	matched = CheckDeviceType(annos, "H100")
	fmt.Println("check result:", matched)
	matched = CheckDeviceType(annos, "4080")
	fmt.Println("check result:", matched)
}
