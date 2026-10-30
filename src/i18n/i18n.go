// Package i18n picks the language of the texts made in Go: log entries,
// statuses and errors shown on the page, the tray, and dialogs. The page
// translates its own texts.
package i18n

import "sync/atomic"

var russian atomic.Bool

// SetLanguage switches to Russian for "ru" and to English for anything else.
func SetLanguage(lang string) { russian.Store(lang == "ru") }

// Russian reports whether texts are in Russian.
func Russian() bool { return russian.Load() }

// L returns the text in the current language.
func L(en, ru string) string {
	if russian.Load() {
		return ru
	}
	return en
}

// Error is an error whose message follows the language at the time it is
// shown, for errors made once at startup. It may wrap another error for
// errors.Is.
type Error struct {
	en, ru string
	err    error
}

// NewError makes an error with an English and a Russian message.
func NewError(en, ru string) *Error { return &Error{en: en, ru: ru} }

// WrapError is NewError that also matches err with errors.Is.
func WrapError(en, ru string, err error) *Error { return &Error{en: en, ru: ru, err: err} }

func (e *Error) Error() string { return L(e.en, e.ru) }
func (e *Error) Unwrap() error { return e.err }
