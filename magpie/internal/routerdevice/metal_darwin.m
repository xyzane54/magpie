//go:build darwin && cgo

#import <Metal/Metal.h>

int router_device_metal(void) {
    @autoreleasepool {
        id<MTLDevice> device = MTLCreateSystemDefaultDevice();
        if (device == nil) return 0;
        [device release];
        return 1;
    }
}
