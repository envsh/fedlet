//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <stdlib.h>
#include <string.h>
#import <AppKit/AppKit.h>

static void copyStr(NSString *s, char **out) {
    *out = NULL;
    if (s == nil) return;
    NSData *d = [s dataUsingEncoding:NSUTF8StringEncoding];
    if (d == nil) return;
    *out = malloc([d length] + 1);
    if (*out == NULL) return;
    memcpy(*out, [d bytes], [d length]);
    (*out)[[d length]] = '\0';
}

// clip_owner resolves the pasteboard source best-effort:
//  1. cooperative marker org.nspasteboard.source (bundle id) if set;
//  2. otherwise the frontmost app at detection time (approximation,
//     the same heuristic used by Awesome Copy / Pesty / Maccy).
// Returns PID (0 when unknown), fills *name_out / *path_out with heap-allocated
// strings (caller must free()).
static long clip_owner(char **name_out, char **path_out) {
    *name_out = NULL;
    *path_out = NULL;
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSString *src = [pb stringForType:@"org.nspasteboard.source"];
        if ([src length] > 0) {
            NSWorkspace *ws = [NSWorkspace sharedWorkspace];
            NSURL *url = [ws URLForApplicationWithBundleIdentifier:src];
            NSString *path = [[NSBundle bundleWithURL:url] executablePath];
            NSString *name = [path lastPathComponent];
            if (name == nil || [name length] == 0) {
                name = [[NSFileManager defaultManager] displayNameAtPath:[url path]];
                path = [url path];
            }
            if (name == nil || [name length] == 0) {
                name = src;
                path = nil;
            }
            copyStr(name, name_out);
            copyStr(path, path_out);
            return 0;
        }
        NSRunningApplication *app = [[NSWorkspace sharedWorkspace] frontmostApplication];
        if (app != nil) {
            NSString *path = [[app executableURL] path];
            NSString *name = [path lastPathComponent];
            if (name == nil || [name length] == 0) {
                name = [app localizedName];
                path = nil;
            }
            copyStr(name, name_out);
            copyStr(path, path_out);
            return (long)[app processIdentifier];
        }
    }
    return 0;
}
*/
import "C"

import "unsafe"

func clipOwner() (int, string, string) {
	var cname, cpath *C.char
	pid := int(C.clip_owner(&cname, &cpath))
	if cpath != nil {
		defer C.free(unsafe.Pointer(cpath))
	}
	if cname != nil {
		defer C.free(unsafe.Pointer(cname))
		return pid, C.GoString(cname), C.GoString(cpath)
	}
	return pid, "", ""
}
