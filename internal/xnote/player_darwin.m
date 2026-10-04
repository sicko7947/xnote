#import <Foundation/Foundation.h>
#import <AVFoundation/AVFoundation.h>
static AVAudioPlayer *player = nil;
void xnote_audio_close(void) { @autoreleasepool { [player stop]; [player release]; player = nil; } }
int xnote_audio_open(const char *path) { @autoreleasepool {
    xnote_audio_close();
    NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
    NSError *error = nil;
    player = [[AVAudioPlayer alloc] initWithContentsOfURL:url error:&error];
    if (!player) return 0;
    player.enableRate = YES;
    [player prepareToPlay];
    return [player play] ? 1 : 0;
} }
void xnote_audio_toggle(void) { if (player.isPlaying) [player pause]; else [player play]; }
void xnote_audio_seek(double seconds) { if (player) player.currentTime = MAX(0, MIN(seconds, player.duration)); }
void xnote_audio_rate(float rate) { if (player) player.rate = rate; }
double xnote_audio_time(void) { return player ? player.currentTime : 0; }
double xnote_audio_duration(void) { return player ? player.duration : 0; }
int xnote_audio_playing(void) { return player && player.isPlaying; }
