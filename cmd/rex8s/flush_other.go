//go:build !darwin

package main

func flushInput() {}

func pasteMode() func() { return func() {} }
