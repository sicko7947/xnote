package xnote

import (
	"fmt"

	"github.com/rivo/tview"
)

func (d *desktop) confirmQueueDownloaded() {
	rows, err := d.s.Records()
	if err != nil {
		d.message(err)
		return
	}
	var ids []string
	for _, r := range rows {
		if canQueueDownloaded(r) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		d.setNotice(d.t("batch_transcribe_empty"))
		return
	}
	c := d.s.Config()
	modal := tview.NewModal().SetText(fmt.Sprintf(d.t("batch_transcribe_confirm"), len(ids), c.Provider, EffectiveTranscriptionConcurrency(c))).
		AddButtons([]string{d.t("cancel"), d.t("queue_transcription")})
	modal.SetDoneFunc(func(index int, _ string) {
		d.closeModal()
		if index != 1 {
			return
		}
		// Provider changes while the dialog is open require a fresh review.
		if current := d.s.Config(); current.Provider != c.Provider || current.Language != c.Language || current.DOWAYPublicUpload != c.DOWAYPublicUpload ||
			current.APIURL != c.APIURL || current.APIKeyEnv != c.APIKeyEnv || current.Model != c.Model || current.OfflineModel != c.OfflineModel {
			d.setNotice(d.t("batch_settings_changed"))
			return
		}
		n, err := d.s.QueueDownloaded(ids)
		d.refresh()
		if err != nil {
			d.message(fmt.Errorf("%s: %w", fmt.Sprintf(d.t("batch_transcribe_queued"), n), err))
			return
		}
		d.setNotice(fmt.Sprintf(d.t("batch_transcribe_queued"), n))
	})
	modal.SetFocus(0)
	d.popup(modal, 76, 14)
}

func (d *desktop) toggleTranscriptionPause() {
	c := d.s.Config()
	c.TranscriptionPaused = !c.TranscriptionPaused
	if err := d.s.SaveConfig(c); err != nil {
		d.message(err)
		return
	}
	d.refresh()
	if c.TranscriptionPaused {
		d.setNotice(d.t("queue_paused_help"))
	} else {
		d.setNotice(d.t("queue_resumed"))
	}
}

// Only fields edited in this dialog replace fresh configuration values. A
// device reconnect or a separate CLI setting change must not be overwritten.
func (s *Store) saveGeneralSettings(original, edited Config) error {
	current := s.Config()
	if edited.Locale != original.Locale {
		current.Locale = edited.Locale
	}
	if edited.Provider != original.Provider {
		current.Provider = edited.Provider
	}
	if edited.Language != original.Language {
		current.Language = edited.Language
	}
	if edited.Auto != original.Auto {
		current.Auto = edited.Auto
	}
	if edited.AutoTranscribe != original.AutoTranscribe {
		current.AutoTranscribe = edited.AutoTranscribe
	}
	if edited.TranscriptionConcurrency != original.TranscriptionConcurrency {
		current.TranscriptionConcurrency = edited.TranscriptionConcurrency
	}
	if edited.APIURL != original.APIURL {
		current.APIURL = edited.APIURL
	}
	if edited.APIKeyEnv != original.APIKeyEnv {
		current.APIKeyEnv = edited.APIKeyEnv
	}
	if edited.Model != original.Model {
		current.Model = edited.Model
	}
	return s.SaveConfig(current)
}
