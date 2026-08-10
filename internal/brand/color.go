package brand

import (
	"fmt"
	"math"
	"strings"
)

// RGB is an 8-bit-per-channel sRGB color.
type RGB struct{ R, G, B uint8 }

// Foreground colors used on top of the accent. They are near-black and
// near-white rather than pure, which reads less harshly without meaningfully
// changing contrast.
var (
	fgLight = RGB{0xFF, 0xFF, 0xFF}
	fgDark  = RGB{0x10, 0x10, 0x1A}

	// Surface colors the derived tints are mixed against.
	surfaceDark = RGB{0x12, 0x12, 0x16}
)

// minContrast is the WCAG AA ratio for normal text.
const minContrast = 4.5

// ParseHex accepts #rgb or #rrggbb, with or without the leading '#'.
func ParseHex(s string) (RGB, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(s), "#")
	switch len(raw) {
	case 3:
		// Expand shorthand by duplicating each nibble: #abc -> #aabbcc.
		raw = string([]byte{raw[0], raw[0], raw[1], raw[1], raw[2], raw[2]})
	case 6:
	default:
		return RGB{}, fmt.Errorf("expected #rgb or #rrggbb, got %q", s)
	}
	var c RGB
	if _, err := fmt.Sscanf(strings.ToLower(raw), "%02x%02x%02x", &c.R, &c.G, &c.B); err != nil {
		return RGB{}, fmt.Errorf("expected #rgb or #rrggbb, got %q", s)
	}
	return c, nil
}

// Hex renders the color as #rrggbb.
func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// relativeLuminance implements the WCAG 2.x definition, which linearises each
// channel before weighting them for human perception.
func (c RGB) relativeLuminance() float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// Contrast returns the WCAG contrast ratio between two colors, from 1 to 21.
func Contrast(a, b RGB) float64 {
	la, lb := a.relativeLuminance(), b.relativeLuminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// mix blends two colors in linear light. Mixing in raw sRGB darkens midtones
// noticeably, which makes generated hover states look muddy.
func mix(a, b RGB, t float64) RGB {
	toLinear := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	toSRGB := func(v float64) uint8 {
		v = math.Max(0, math.Min(1, v))
		var s float64
		if v <= 0.0031308 {
			s = v * 12.92
		} else {
			s = 1.055*math.Pow(v, 1/2.4) - 0.055
		}
		return uint8(math.Round(math.Max(0, math.Min(1, s)) * 255))
	}
	return RGB{
		R: toSRGB(toLinear(a.R)*(1-t) + toLinear(b.R)*t),
		G: toSRGB(toLinear(a.G)*(1-t) + toLinear(b.G)*t),
		B: toSRGB(toLinear(a.B)*(1-t) + toLinear(b.B)*t),
	}
}

// mixGamma blends two colors in plain sRGB space.
//
// Used for both derived tints. Mixing toward white or toward the surface in
// linear light is luminance-correct but strips hue, turning a green accent
// grey and a blue one slate. Designers pick these values by eye in sRGB, so
// blending the same way is what reproduces what they drew.
func mixGamma(a, b RGB, t float64) RGB {
	blend := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x)*(1-t) + float64(y)*t))
	}
	return RGB{R: blend(a.R, b.R), G: blend(a.G, b.G), B: blend(a.B, b.B)}
}

// bestForeground picks whichever of near-white or near-black is more legible on
// the given background, and reports the ratio it achieved so callers can warn.
func bestForeground(bg RGB) (RGB, float64) {
	light := Contrast(bg, fgLight)
	dark := Contrast(bg, fgDark)
	if dark > light {
		return fgDark, dark
	}
	return fgLight, light
}

// Palette is the resolved set of accent-derived colors for one color scheme.
type Palette struct {
	Accent      RGB
	AccentHover RGB
	AccentFg    RGB
	Subtle      RGB

	// Contrast is the ratio between Accent and AccentFg. Below minContrast the
	// operator's brand color is hard to put text on.
	Contrast float64
}

// darkPalette derives the portal's colors. A brand color chosen for white
// backgrounds is usually too dark on a dark surface, so when the operator has
// not supplied an explicit dark accent we lighten theirs until it clears AA
// against the dark surface. Giving up after a bounded number of steps keeps a
// pathological input (pure black, say) from looping.
func darkPalette(accent RGB, explicit bool) Palette {
	if !explicit {
		for i := 0; i < 20 && Contrast(accent, surfaceDark) < minContrast; i++ {
			accent = mix(accent, fgLight, 0.10)
		}
	}
	fg, ratio := bestForeground(accent)
	return Palette{
		Accent: accent,
		// 0.18 toward white for hover, 0.66 toward the surface for the subtle
		// tint. The tint stops well short of the surface on purpose: past about
		// three quarters it reads as a neutral panel rather than as an accent,
		// which defeats the token.
		AccentHover: mixGamma(accent, fgLight, 0.18),
		AccentFg:    fg,
		Subtle:      mixGamma(accent, surfaceDark, 0.66),
		Contrast:    ratio,
	}
}
