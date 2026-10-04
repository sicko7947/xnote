package xnote

/*
#cgo LDFLAGS: -framework Foundation -framework AVFoundation
#include <stdlib.h>
int xnote_audio_open(const char *path);
void xnote_audio_close(void);
void xnote_audio_toggle(void);
void xnote_audio_seek(double seconds);
void xnote_audio_rate(float rate);
double xnote_audio_time(void);
double xnote_audio_duration(void);
int xnote_audio_playing(void);
*/
import "C"
import (
	"errors"
	"sync"
	"unsafe"
)

var playerMutex sync.Mutex

func PlayerOpen(path string) error {
	playerMutex.Lock()
	defer playerMutex.Unlock()
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	if C.xnote_audio_open(p) == 0 {
		return errors.New("macOS could not open this audio file")
	}
	return nil
}
func PlayerClose()  { playerMutex.Lock(); defer playerMutex.Unlock(); C.xnote_audio_close() }
func PlayerToggle() { playerMutex.Lock(); defer playerMutex.Unlock(); C.xnote_audio_toggle() }
func PlayerSeek(seconds float64) {
	playerMutex.Lock()
	defer playerMutex.Unlock()
	C.xnote_audio_seek(C.double(seconds))
}
func PlayerRate(rate float64) {
	playerMutex.Lock()
	defer playerMutex.Unlock()
	C.xnote_audio_rate(C.float(rate))
}
func PlayerPosition() (float64, float64, bool) {
	playerMutex.Lock()
	defer playerMutex.Unlock()
	return float64(C.xnote_audio_time()), float64(C.xnote_audio_duration()), C.xnote_audio_playing() != 0
}
