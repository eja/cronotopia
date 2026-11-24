// Copyright (C) by Ubaldo Porcheddu <ubaldo@eja.it>

package acta

import (
	"fmt"
)

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

func GetJulianDayBounds(year, month, day int) (int64, int64) {
	if month == 0 && day == 0 {
		return DateToJulianDay(year, 1, 1), DateToJulianDay(year, 12, 31)
	}
	if day == 0 {
		lastDay := 31
		switch month {
		case 2:
			if (year%4 == 0 && year%100 != 0) || (year%400 == 0) {
				lastDay = 29
			} else {
				lastDay = 28
			}
		case 4, 6, 9, 11:
			lastDay = 30
		}
		return DateToJulianDay(year, month, 1), DateToJulianDay(year, month, lastDay)
	}
	jd := DateToJulianDay(year, month, day)
	return jd, jd
}

func GetTargetJulianDay(p SearchParams) int64 {
	m := p.Month
	d := p.Day
	if p.Range > 0 && p.Year != 0 {
		if m == 0 && d == 0 {
			m = 12
			d = 31
		} else if d == 0 {
			switch m {
			case 2:
				d = 28
			case 4, 6, 9, 11:
				d = 30
			default:
				d = 31
			}
		}
	}
	return DateToJulianDay(p.Year, m, d)
}
