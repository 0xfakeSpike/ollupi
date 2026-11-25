//go:build darwin && cgo

package sandbox

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#import <Foundation/Foundation.h>

static char *trash_item(const char *path) {
	@autoreleasepool {
		NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
		NSError *err = nil;
		if ([[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:nil error:&err]) {
			return NULL;
		}
		return strdup([[err localizedDescription] UTF8String]);
	}
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// trash moves path to the Trash as Finder does, so that Finder puts it
// back where it was.
func trash(path, _ string) error {
	c := C.CString(path)
	defer C.free(unsafe.Pointer(c))
	if msg := C.trash_item(c); msg != nil {
		defer C.free(unsafe.Pointer(msg))
		return fmt.Errorf("sandbox: move %s to the Trash: %s", path, C.GoString(msg))
	}
	return nil
}
