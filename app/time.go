// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package main

import "fmt"

func DateToJulianDay(year, month, day int) int64 {
	if month == 0 {
		month = 1
	}
	if day == 0 {
		day = 1
	}
	a := (14 - month) / 12
	y := year + 4800 - a
	m := month + 12*a - 3
	return int64(day + (153*m+2)/5 + 365*y + y/4 - y/100 + y/400 - 32045)
}

func FormatDateTime(y, m, d int) string {
	if y == 0 && m == 0 && d == 0 {
		return ""
	}
	var datePart string
	if y < 0 {
		datePart = fmt.Sprintf("-%04d", -y)
	} else {
		datePart = fmt.Sprintf("%04d", y)
	}
	if m > 0 {
		datePart += fmt.Sprintf("-%02d", m)
		if d > 0 {
			datePart += fmt.Sprintf("-%02d", d)
		}
	}
	return datePart
}
