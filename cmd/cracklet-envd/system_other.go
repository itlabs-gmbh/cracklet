//go:build !linux

package main

import "errors"

type linuxSystem struct{}

var errLinuxOnly = errors.New("cracklet-envd only runs on Linux guests")

func (linuxSystem) ReplaceAddress(string, string) error      { return errLinuxOnly }
func (linuxSystem) ReplaceDefaultRoute(string, string) error { return errLinuxOnly }
func (linuxSystem) SetHostname(string) error                 { return errLinuxOnly }
func (linuxSystem) SetTime(int64) error                      { return errLinuxOnly }
func (linuxSystem) SeedRandom([]byte) error                  { return errLinuxOnly }
