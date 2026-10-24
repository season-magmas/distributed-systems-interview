package main

import "time"

func recoveryWindow(requested, normal, hang time.Duration) time.Duration { return requested }
