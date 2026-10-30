// Package ui holds the app's web pages, built into the exe.
package ui

import (
	_ "embed"
	"strings"
)

var (
	//go:embed index.html
	index string
	//go:embed overlay.html
	overlay string
	//go:embed chroma.js
	chromaJS string
)

// Both pages carry this placeholder where the shared chroma key code goes.
const chromaPlaceholder = "/*chroma.js*/"

var (
	// Index is the main window's page.
	Index = strings.Replace(index, chromaPlaceholder, chromaJS, 1)
	// Overlay is the page that shows videos and GIFs: in an OBS browser
	// source, or in the window the app opens for window capture.
	Overlay = []byte(strings.Replace(overlay, chromaPlaceholder, chromaJS, 1))
)
