# DOWAY recording transcription

The DOWAY provider submits a downloaded MP3 using the signed-in account and
its associated recorder. Selecting DOWAY does not enable automatic
transcription: download-only remains the default. The TUI's `t` action queues
the selected recording. Choose the source language in settings first. Supported
app-derived mappings include Chinese, English, Japanese, Korean, French,
Spanish, Russian, German, Italian, Vietnamese, and Arabic.
There is no provider fallback.

DOWAY additionally requires an explicit upload permission in its provider
settings (`doway_public_upload`, off by default). Its transcription service
needs a directly downloadable audio object, as used by the original app. While
processing, anyone holding that unguessable object link can read it. Audio and
result objects are deleted after a terminal outcome and local result caching.
An interrupted process retains its task journal; resume it to finish processing
and cleanup. The local recording is kept.

## Request flow

1. Read `/api/device/get_device_info` and `/api/player/get` to check the account,
   recorder, region, and storage bucket.
2. Persist a per-record job before sending `/api/device/verify_device`. The
   server decides whether the account has enough transcription allowance.
3. Obtain short-lived OSS credentials from `/api/player/getAliyunToken` and,
   after explicit permission, stream the original MP3 with the app's
   `public-read` ACL and a unique random object key.
4. Submit `/api/audio/start_asr` once and persist the returned order ID.
5. Poll `/api/audio/get_result` at 30-second intervals, retrieving the result
   from authenticated OSS storage when directed by the server.
6. Cache the transcript, send the original completion/usage report once, and
   save the transcript and timestamped segments through the normal recording
   pipeline. Clean up only the objects belonging to that task.

The job and result cache live in the recording's `.transcription` directory.
They bind the request to the audio SHA-256, account, device, and language. A
process lock prevents two local processes from submitting the same recording
concurrently. Accepted or uncertain submissions are queried using their saved
identity where the server returned an order ID; they are not blindly submitted
again. Without an order ID, an uncertain submission may need to be checked in
the original app. An interrupted allowance
verification is reported as uncertain because the API provides no proven
idempotency contract for that operation.

## Protocol evidence

Requests use the DOWAY HTTPS API, including for devices reporting area 0; the
device's area and China storage bucket are preserved. The old app's cleartext
China URL is not used.

The field mappings were checked against the supplied DOWAY 3.7.7 Android
installation package's decompiled Dart assembly:

- `record_http_helper.dart`: device verification fields and business rejection
  handling; the recording duration is sent in whole seconds.
- `transcription_helper.dart`, `LangMode.dart`, and language configuration:
  verification uses the recording's previous language (default `en`) before
  selecting the new file transcription language (`cn`, `en`, or `ja`).
- `area_utils.dart` and `aliyun_oss_transcription_mgr.dart`: device area and
  account region determine the audio storage bucket. The ASR `type` field is
  the area enum, not the transcription-engine enum.
- `ifly_long_asr_server_mgr.dart`: submission and result polling fields,
  version 2 OSS result lookup, and version 1 read-only result query when the
  object is absent.
- `xf_asr_helper.dart` and `translate_info.dart`: compact result fields
  `a` (text), `d`/`e` (start/end milliseconds), and `r` (speaker).

HTTP task statuses are `0` (created), `3` (processing), and `4` (complete).
Unknown formats and failed statuses are reported instead of producing a
partial transcript. The implementation never silently enables public uploads
or copies the original app's automatic repeated submission requests.

## Verification scope

Unit tests cover the request contracts, parsing, account/device identity,
upload permission, cancellation, and persisted task recovery. A successful
short sample demonstrates connectivity and one transcription; it does not
establish long-recording limits or every language and account plan.

On 2026-10-05, a 7-second synthetic sample passed allowance verification,
upload, task acceptance, and actual transcript parsing through the HTTPS
endpoint with area 0. With the original app's upload ACL it completed in 23
seconds, returning the expected sentence and a 0–7.14 second segment. The same
sample uploaded privately had failed with provider `failType=2` (transcoding).
All diagnostic audio and result objects were deleted afterward. No personal
recordings were used. This comparison is why upload permission is explicit.
The same completed synthetic task also received a successful acknowledgement
from the normal 18-field completion/usage report, without another ASR submission.
The separate ElevenLabs path returned correct text and timestamped segments
from the same sample with no language hint. A second synthetic file combining
Chinese, English, and Japanese returned all three source languages and three
timestamped segments in Auto mode.

The original app also has a separate failed-task reporting endpoint. Its full
accounting contract has not been validated; this client records terminal
failures and cleans up their objects but does not send that separate report.
