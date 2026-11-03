//go:build windows

package sandboxfs

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRemoveSandboxACEsPreservesOtherPermissions(t *testing.T) {
	const target = "S-1-15-2-1-2-3-4-5-6-7"
	// Explicit and inherited allow ACEs for this invocation, a deny ACE, and SYSTEM.
	sd, err := windows.SecurityDescriptorFromString("D:(D;;FW;;;" + target + ")(A;;FA;;;SY)(A;;FR;;;" + target + ")(A;ID;FW;;;" + target + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := windows.StringToSid(target)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		kind, flags uint8
		mask        windows.ACCESS_MASK
		sid         string
	}
	entries := func() []entry {
		var result []entry
		for i := uint32(0); i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, i, &ace); err != nil {
				t.Fatal(err)
			}
			result = append(result, entry{ace.Header.AceType, ace.Header.AceFlags, ace.Mask, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()})
		}
		return result
	}
	original := entries()
	if len(original) != 4 {
		t.Fatalf("expected four fixture ACEs: %v", original)
	}
	if err := removeSandboxACEs(acl, sid); err != nil {
		t.Fatal(err)
	}
	got := entries()
	if len(got) != 2 || got[0] != original[0] || got[1] != original[1] {
		t.Fatalf("unrelated ACEs changed: before=%v after=%v", original, got)
	}
	// Cleanup may encounter an original handle again after walking the same file.
	if err := removeSandboxACEs(acl, sid); err != nil {
		t.Fatal(err)
	}
	if acl.AceCount != 2 {
		t.Fatal("second revocation changed ACL")
	}
}
