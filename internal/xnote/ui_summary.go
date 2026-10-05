package xnote

import (
	"fmt"
	"strconv"

	"github.com/rivo/tview"
)

func (d *desktop) summarySettings() {
	c := d.s.Config()
	original := c
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" " + d.t("summary_settings") + " ")
	form.AddTextView("DOWAY", d.t("summary_provider_help"), 72, 4, false, false)
	form.AddFormItem(newOptionCheckbox(d.t("automatic_summary"), c.AutoSummary, func(v bool) { c.AutoSummary = v }))
	languages := []string{"", "zh-CN", "en", "ja"}
	selected := 0
	for i, language := range languages {
		if language == c.SummaryLanguage {
			selected = i
		}
	}
	form.AddDropDown(d.t("summary_language"), []string{d.t("summary_follow_locale"), "简体中文", "English", "日本語"}, selected, func(_ string, i int) { c.SummaryLanguage = languages[i] })
	concurrency := strconv.Itoa(EffectiveSummaryConcurrency(c))
	form.AddInputField(d.t("summary_concurrency"), concurrency, 4, nil, func(v string) { concurrency = v })
	form.AddFormItem(newOptionCheckbox(d.t("summary_thinking"), c.SummaryThinking, func(v bool) { c.SummaryThinking = v }))
	form.AddFormItem(newOptionCheckbox(d.t("summary_enabled"), !c.SummaryDisabled, func(v bool) { c.SummaryDisabled = !v }))
	form.AddFormItem(newOptionCheckbox(d.t("mindmap_enabled"), c.MindmapEnabled, func(v bool) { c.MindmapEnabled = v }))
	form.AddButton(d.t("save"), func() {
		n, err := strconv.Atoi(concurrency)
		if err != nil || n < 1 || n > 8 {
			d.setNotice(d.t("summary_concurrency_invalid"))
			form.SetFocus(3)
			d.app.SetFocus(form)
			return
		}
		c.SummaryConcurrency = n
		current := d.s.Config()
		if c.AutoSummary != original.AutoSummary {
			current.AutoSummary = c.AutoSummary
		}
		if c.SummaryLanguage != original.SummaryLanguage {
			current.SummaryLanguage = c.SummaryLanguage
		}
		if c.SummaryConcurrency != original.SummaryConcurrency {
			current.SummaryConcurrency = c.SummaryConcurrency
		}
		if c.SummaryThinking != original.SummaryThinking {
			current.SummaryThinking = c.SummaryThinking
		}
		if c.SummaryDisabled != original.SummaryDisabled {
			current.SummaryDisabled = c.SummaryDisabled
		}
		if c.MindmapEnabled != original.MindmapEnabled {
			current.MindmapEnabled = c.MindmapEnabled
		}
		if err := d.s.SaveConfig(current); err != nil {
			d.message(err)
			return
		}
		d.closeModal()
	})
	form.AddButton(d.t("back"), d.closeModal)
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 90, 26)
}

func (d *desktop) queueSummary(id string) {
	if err := d.s.QueueSummary(id); err != nil {
		d.message(err)
		return
	}
	d.refresh()
	r, err := d.s.Get(id)
	if err != nil {
		d.message(err)
		return
	}
	switch r.SummaryState {
	case "done":
		d.setNotice(d.t("summary_unchanged"))
	case "running":
		d.setNotice(d.t("summary_running"))
	default:
		d.setNotice(d.t("summary_queued"))
	}
}

func (d *desktop) confirmQueueSummaries() {
	rows, err := d.s.Records()
	if err != nil {
		d.message(err)
		return
	}
	var ids []string
	c := d.s.Config()
	for _, r := range rows {
		if SummaryEligibleForBatch(r, c) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		d.setNotice(d.t("batch_summary_empty"))
		return
	}
	language := c.SummaryLanguage
	if language == "" {
		language = c.Locale
	}
	mode := d.t("summary_fast")
	if c.SummaryThinking {
		mode = d.t("summary_deep")
	}
	modal := tview.NewModal().SetText(fmt.Sprintf(d.t("batch_summary_confirm"), len(ids), language, mode, EffectiveSummaryConcurrency(c))).
		AddButtons([]string{d.t("cancel"), d.t("queue_summary")})
	modal.SetDoneFunc(func(index int, _ string) {
		d.closeModal()
		if index != 1 {
			return
		}
		current := d.s.Config()
		if current.SummaryDisabled != c.SummaryDisabled || current.MindmapEnabled != c.MindmapEnabled || current.SummaryThinking != c.SummaryThinking || current.SummaryLanguage != c.SummaryLanguage || current.SummaryLanguage == "" && current.Locale != c.Locale {
			d.setNotice(d.t("batch_summary_settings_changed"))
			return
		}
		n, err := d.s.QueueSummaries(ids)
		d.refresh()
		if err != nil {
			d.message(fmt.Errorf("%s: %w", fmt.Sprintf(d.t("batch_summary_queued"), n), err))
			return
		}
		d.setNotice(fmt.Sprintf(d.t("batch_summary_queued"), n))
	})
	modal.SetFocus(0)
	d.popup(modal, 76, 14)
}
