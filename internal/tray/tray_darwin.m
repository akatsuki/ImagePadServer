//go:build darwin && cgo
// +build darwin,cgo

#import <Cocoa/Cocoa.h>
#import <dispatch/dispatch.h>

extern void imagepadDarwinOpen(void);
extern void imagepadDarwinReconnect(void);
extern void imagepadDarwinExit(void);
void imagepadStopStatusItem(void);

@interface ImagePadStatusItemController : NSObject
@end

static NSStatusItem *imagepadStatusItem = nil;
static ImagePadStatusItemController *imagepadStatusController = nil;
static NSString *imagepadVersion = nil;
static NSString *imagepadCopyright = nil;

@implementation ImagePadStatusItemController
- (void)about:(id)sender {
	(void)sender;
	NSMutableDictionary *options = [NSMutableDictionary dictionary];
	options[NSAboutPanelOptionApplicationName] = @"ImagePadServer";
	if (imagepadVersion != nil) {
		options[NSAboutPanelOptionApplicationVersion] = imagepadVersion;
		options[@"Version"] = imagepadVersion;
	}
	if (imagepadCopyright != nil) {
		options[@"Copyright"] = imagepadCopyright;
	}
	if (imagepadStatusItem != nil && imagepadStatusItem.button.image != nil) {
		options[NSAboutPanelOptionApplicationIcon] = imagepadStatusItem.button.image;
	}
	[NSApp orderFrontStandardAboutPanelWithOptions:options];
	[NSApp activateIgnoringOtherApps:YES];
}

- (void)open:(id)sender {
	(void)sender;
	imagepadDarwinOpen();
}

- (void)reconnect:(id)sender {
	(void)sender;
	imagepadDarwinReconnect();
}

- (void)quit:(id)sender {
	(void)sender;
	imagepadDarwinExit();
	imagepadStopStatusItem();
}
@end

static NSImage *imagepadTemplateImage(void *imageBytes, int imageLen) {
	if (imageBytes == NULL || imageLen <= 0) {
		return nil;
	}
	NSData *data = [NSData dataWithBytes:imageBytes length:(NSUInteger)imageLen];
	NSImage *image = [[NSImage alloc] initWithData:data];
	if (image == nil) {
		return nil;
	}
	[image setTemplate:YES];
	[image setSize:NSMakeSize(18, 18)];
	return image;
}

static void imagepadInstallStatusItem(char *title, char *version, char *copyright, void *imageBytes, int imageLen) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

	if (imagepadStatusItem != nil) {
		return;
	}

	imagepadVersion = [[NSString stringWithUTF8String:version] copy];
	imagepadCopyright = [[NSString stringWithUTF8String:copyright] copy];

	imagepadStatusController = [[ImagePadStatusItemController alloc] init];
	imagepadStatusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
	NSImage *image = imagepadTemplateImage(imageBytes, imageLen);
	if (image != nil) {
		imagepadStatusItem.button.image = image;
	} else {
		imagepadStatusItem.button.title = [NSString stringWithUTF8String:title];
	}
	imagepadStatusItem.button.toolTip = @"ImagePadServer";

	NSMenu *menu = [[NSMenu alloc] initWithTitle:@"ImagePadServer"];
	[menu addItemWithTitle:@"ImagePadServerについて" action:@selector(about:) keyEquivalent:@""].target = imagepadStatusController;
	[menu addItem:[NSMenuItem separatorItem]];
	[menu addItemWithTitle:@"開く" action:@selector(open:) keyEquivalent:@""].target = imagepadStatusController;
	[menu addItemWithTitle:@"再接続" action:@selector(reconnect:) keyEquivalent:@""].target = imagepadStatusController;
	[menu addItem:[NSMenuItem separatorItem]];
	[menu addItemWithTitle:@"終了" action:@selector(quit:) keyEquivalent:@""].target = imagepadStatusController;
	imagepadStatusItem.menu = menu;
}

void imagepadStartStatusItem(char *title, char *version, char *copyright, void *imageBytes, int imageLen) {
	imagepadInstallStatusItem(title, version, copyright, imageBytes, imageLen);
	[NSApp run];
}

void imagepadStopStatusItem(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (imagepadStatusItem != nil) {
			[[NSStatusBar systemStatusBar] removeStatusItem:imagepadStatusItem];
			imagepadStatusItem = nil;
		}
		[NSApp stop:nil];
		NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
			location:NSMakePoint(0, 0)
			modifierFlags:0
			timestamp:0
			windowNumber:0
			context:nil
			subtype:0
			data1:0
			data2:0];
		[NSApp postEvent:event atStart:NO];
	});
}
