//go:build linux && webkit2_41 && cgo

package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <stdlib.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

extern int dynaHostOpen(char *origin, char *target);
extern int dynaHostMedia(char *origin, char *page, int audio, int video, int display);

static void host_free(gpointer data, GClosure *closure) { g_free(data); }
static void host_ready(WebKitWebView *view, gpointer window) {
	gtk_widget_show_all(GTK_WIDGET(window));
}
static void host_close(WebKitWebView *view, gpointer window) {
	gtk_widget_destroy(GTK_WIDGET(window));
}

static GtkWidget *host_create(WebKitWebView *view, WebKitNavigationAction *action, gpointer origin) {
	const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(action));
	if (uri == NULL || dynaHostOpen(origin, (char *)uri) != 1) return NULL;

	// WebKitWebView.cpp:981 uses the opener's configuration even with a new
	// content manager, so related views share cookies and script handlers.
	// Wails reads the main view's URI (window.c:55-56); its origin check
	// (frontend.go:260-266) rejects the server origin for both views.
	// Keep BindingsAllowedOrigins empty. Recheck this guard on Wails upgrades.
	GtkWidget *related = webkit_web_view_new_with_related_view(view);
	GtkWidget *window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	gtk_window_set_title(GTK_WINDOW(window), "Dyna computer");
	gtk_window_set_default_size(GTK_WINDOW(window), 1100, 760);
	gtk_container_add(GTK_CONTAINER(window), related);
	g_signal_connect_data(related, "create", G_CALLBACK(host_create), g_strdup(origin), host_free, 0);
	g_signal_connect(related, "ready-to-show", G_CALLBACK(host_ready), window);
	g_signal_connect(related, "close", G_CALLBACK(host_close), window);
	return related;
}

// Wails answers no permission requests, and WebKitGTK denies a request
// nobody answers. Only user-media requests are decided here; others keep
// that default. The request names no frame, so dynaHostMedia sees the
// window's page; see hostMediaAllowed.
static gboolean host_permission(WebKitWebView *view, WebKitPermissionRequest *request, gpointer origin) {
	if (!WEBKIT_IS_USER_MEDIA_PERMISSION_REQUEST(request)) return FALSE;
	WebKitUserMediaPermissionRequest *media = WEBKIT_USER_MEDIA_PERMISSION_REQUEST(request);
	const char *page = webkit_web_view_get_uri(view);
	if (page != NULL && dynaHostMedia(origin, (char *)page,
		webkit_user_media_permission_is_for_audio_device(media),
		webkit_user_media_permission_is_for_video_device(media),
		webkit_user_media_permission_is_for_display_device(media)) == 1)
		webkit_permission_request_allow(request);
	else
		webkit_permission_request_deny(request);
	return TRUE;
}

static WebKitWebView *host_view(GtkWidget *widget) {
	if (WEBKIT_IS_WEB_VIEW(widget)) return WEBKIT_WEB_VIEW(widget);
	if (!GTK_IS_CONTAINER(widget)) return NULL;
	WebKitWebView *view = NULL;
	GList *children = gtk_container_get_children(GTK_CONTAINER(widget));
	for (GList *item = children; item != NULL && view == NULL; item = item->next)
		view = host_view(GTK_WIDGET(item->data));
	g_list_free(children);
	return view;
}

static gboolean host_install(gpointer origin) {
	WebKitWebView *view = NULL;
	GList *windows = gtk_window_list_toplevels();
	for (GList *item = windows; item != NULL && view == NULL; item = item->next)
		view = host_view(GTK_WIDGET(item->data));
	g_list_free(windows);
	if (view != NULL) {
		g_signal_connect_data(view, "create", G_CALLBACK(host_create), origin, host_free, 0);
		g_signal_connect_data(view, "permission-request", G_CALLBACK(host_permission), g_strdup(origin), host_free, 0);
	} else {
		g_printerr("Dyna window host: no WebView found; links and pop-outs are unavailable\n");
		g_free(origin);
	}
	return G_SOURCE_REMOVE;
}

static void host_start(char *origin) {
	g_idle_add(host_install, g_strdup(origin));
}
*/
import "C"

import (
	"log"
	"os/exec"
	"unsafe"
)

func startWindowHost(addr string) {
	origin := C.CString("http://" + addr)
	defer C.free(unsafe.Pointer(origin))
	C.host_start(origin)
}

//export dynaHostMedia
func dynaHostMedia(origin, page *C.char, audio, video, display C.int) C.int {
	if hostMediaAllowed(C.GoString(origin), C.GoString(page), audio != 0, video != 0, display != 0) {
		return 1
	}
	return 0
}

//export dynaHostOpen
func dynaHostOpen(origin, target *C.char) C.int {
	u := C.GoString(target)
	action := hostURLAction(C.GoString(origin), u)
	if action == hostExternal {
		go func() {
			if err := exec.Command("xdg-open", u).Run(); err != nil {
				log.Printf("Open browser: %v", err)
			}
		}()
	}
	return C.int(action)
}
