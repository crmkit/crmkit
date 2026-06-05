package auth

import "fmt"

// Email is an outbound message carrying both a plain-text and a branded HTML
// body. Providers send both (multipart/alternative over SMTP); clients that
// can't render HTML fall back to Text.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// logoSVG is the crmkit mark, inlined so the emails are self-contained (no
// external image fetch). Some clients (notably Gmail) strip inline SVG, so the
// layout is designed to read fine without it - the "crmkit" wordmark below the
// logo is plain text.
const logoSVG = `<svg width="44" height="44" viewBox="0 0 1024 1024" fill="none" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="crmkit"><path d="M751.604 288.401C756.34 288.097 761.295 288.092 766.054 288C780.746 300.028 795.012 318.198 809.324 331.423C811.439 343.118 810.956 365.488 810.945 378.124L810.474 463.824C795.035 463.65 778.021 463.141 762.732 463.933C763.801 447.699 763.169 429.103 763.031 412.686L762.674 339.472C753.88 338.885 743.431 339.51 734.498 339.808C734.855 375.84 734.866 411.873 734.544 447.905C718.324 447.738 702.092 447.848 685.86 448.233V434.863L685.757 339.608C679.813 339.176 664.834 338.562 659.466 340.044C661.282 381.591 660.248 421.863 657.96 463.364C641.613 463.587 625.358 463.323 609 464C611.782 409.792 608.609 346.137 609.77 290.302C623.565 287.623 650.775 289.199 665.398 289.107C693.976 288.927 723.026 289.537 751.604 288.401Z" fill="black"/><path d="M260.14 519L260.779 519.287C264.911 528.652 263.682 604.507 262.4 616.831C277.234 616.716 320.982 615.673 332.9 617.473C332.763 601.504 333.429 584.538 333.816 568.489C348.099 567.732 367.632 567.95 381.83 568.557C382.081 581.867 381.725 596.345 381.649 609.735C371.52 619.525 361.334 629.246 351.087 638.921C361.394 648.7 371.485 658.719 381.351 668.945C382.783 689.166 381.303 709.641 381.939 730L356.337 729.966L332.906 729.989C332.981 708.242 332.892 686.484 332.639 664.737C310.208 665.288 286.128 664.921 263.535 664.932C262.537 687.046 263.976 707.233 262.087 729.45C246.074 729.473 228.929 729.232 213.031 729.874L213 519.367C228.671 519.413 244.494 519.653 260.14 519Z" fill="black"/><path d="M263.094 290.029C273.955 288.489 300.148 289.114 312.257 289.184C333.289 289.303 356.502 288.637 377.309 289.509C377.48 302.46 376.239 328.824 378 339.888C340.398 339.278 300.975 340.015 263.212 340.004C264.759 358.024 264.088 397.657 262.822 415.41C294.872 414.508 328.225 415.091 360.336 415.307C366.693 415.41 370.771 415.017 376.997 414.402C377.679 430.601 377.341 447.671 377.252 463.958C337.512 463.287 295.055 464.051 255.065 463.997C241.435 449.595 227.428 435.552 213.061 421.883L213 332.185C226.371 320.651 240.832 301.864 255.78 291.457C257.143 290.508 261.279 290.155 263.094 290.029Z" fill="black"/><path d="M669.504 519L717.403 519.023C718.275 532.765 717.358 554.532 717.174 568.85C743.52 568.551 770.588 568.908 797.013 568.908C797.506 584.357 796.853 601.05 797.082 616.799C771.378 617.433 743.955 617.087 718.149 617.087C717.197 637.02 717.839 661.092 717.38 681.591L775.322 681.706L810.725 681.602C810.645 697.742 810.736 713.871 811 730L711.912 729.804C701.376 721.353 678.676 697.258 669.699 687.021C668.427 666.811 669.275 638.104 669.298 617.329C669.321 585.187 668.564 550.958 669.504 519Z" fill="black"/><path d="M475.845 289.526C482.443 290.153 496.864 289.607 504.141 289.541L557.719 289C558.224 305.309 557.891 323.092 557.903 339.509C531.182 339.746 503.888 339.226 477.27 340.248C475.995 366.479 476.922 400.277 476.964 427.18C477.103 439.454 477.001 451.729 476.658 464C460.921 463.567 443.774 463.157 428.129 463.956C427.921 420.427 427.964 376.897 428.263 333.368C434.641 324.972 465.496 292.375 475.845 289.526Z" fill="black"/><path d="M489.246 606C500.481 607.845 532.581 604.189 537.874 607.442C542.122 618.721 539.826 667.332 538.092 681.252C559.113 681.033 580.135 681.091 601.167 681.402C602.637 695.806 601.856 713.348 601.144 727.891L601.317 728.387L600.387 729.978C577.689 730.036 554.992 729.978 532.294 729.805C522.834 723.289 497.496 697.237 489.022 688.137C489.333 660.677 488.667 633.748 489.246 606Z" fill="black"/><path d="M489.119 519C504.641 519.717 523.372 519.239 539.145 519.193C538.981 527.999 540.735 563.7 539.636 567.863C522.822 567.625 505.997 567.67 489.174 568C488.477 552.107 490.166 535.212 489.119 519Z" fill="black"/><path d="M943 942H81V80H943V942ZM130 130V893H893V130H130Z" fill="black"/></svg>`

// brandedHTML wraps body content in the shared crmkit email layout: a centered
// white card with the logo + wordmark header and a muted footer. Uses
// table-based layout and inline styles for broad email-client support.
func brandedHTML(heading, bodyHTML string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>` +
		`<body style="margin:0;padding:0;background:#fafafa;">` +
		`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#fafafa;padding:32px 12px;"><tr><td align="center">` +
		`<table role="presentation" cellpadding="0" cellspacing="0" style="width:460px;max-width:460px;background:#ffffff;border:1px solid #ececec;border-radius:14px;">` +
		`<tr><td style="padding:34px 38px;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#111111;">` +
		`<div style="text-align:center;margin-bottom:26px;">` + logoSVG +
		`<div style="font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;font-size:15px;letter-spacing:3px;color:#111111;margin-top:6px;">crmkit</div></div>` +
		`<h1 style="font-size:18px;font-weight:600;margin:0 0 14px;color:#111111;">` + heading + `</h1>` +
		bodyHTML +
		`<div style="margin-top:30px;padding-top:16px;border-top:1px solid #ececec;font-size:12px;line-height:1.5;color:#9a9a9a;">You're receiving this because someone used this address with crmkit. If it wasn't you, you can safely ignore this email.</div>` +
		`</td></tr></table></td></tr></table></body></html>`
}

// codeBlock renders a large, letter-spaced one-time code.
func codeBlock(code string) string {
	return `<div style="font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;font-size:30px;font-weight:700;letter-spacing:8px;text-align:center;background:#f5f5f5;border-radius:10px;padding:18px 0;margin:10px 0;color:#111111;">` + code + `</div>`
}

// button renders a dark call-to-action link.
func button(label, url string) string {
	return `<div style="margin:6px 0 4px;"><a href="` + url + `" style="display:inline-block;background:#111111;color:#ffffff;text-decoration:none;font-size:14px;font-weight:600;padding:12px 24px;border-radius:9px;">` + label + `</a></div>`
}

func para(html string) string {
	return `<p style="font-size:14px;line-height:1.6;color:#444444;margin:0 0 14px;">` + html + `</p>`
}

// LoginEmail is the one-time login code message.
func LoginEmail(to, code string, ttlMinutes int) Email {
	return Email{
		To:      to,
		Subject: "Your crmkit login code",
		Text: fmt.Sprintf("Your crmkit login code is: %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.",
			code, ttlMinutes),
		HTML: brandedHTML("Your login code",
			para("Enter this code to sign in:")+codeBlock(code)+
				fmt.Sprintf(`<p style="font-size:13px;color:#777777;margin:8px 0 0;">Expires in %d minutes.</p>`, ttlMinutes)),
	}
}

// EscalationEmail is the step-up (sensitive action) confirmation code.
func EscalationEmail(to, action, code string, ttlMinutes int) Email {
	return Email{
		To:      to,
		Subject: "crmkit security code",
		Text: fmt.Sprintf("Your authorization code to %s is: %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.",
			action, code, ttlMinutes),
		HTML: brandedHTML("Security code",
			para(fmt.Sprintf("To <strong>%s</strong>, use this code:", action))+codeBlock(code)+
				fmt.Sprintf(`<p style="font-size:13px;color:#777777;margin:8px 0 0;">Expires in %d minutes. If you did not request this, ignore this email.</p>`, ttlMinutes)),
	}
}

// InviteEmail notifies someone they've been invited to a workspace, with the
// login instruction and base URL.
func InviteEmail(to, baseURL string) Email {
	return Email{
		To:      to,
		Subject: "You've been invited to crmkit",
		Text: fmt.Sprintf("You've been invited to a crmkit workspace.\n\nTo accept, sign in at %s using this email address (%s). You'll join automatically.",
			baseURL, to),
		HTML: brandedHTML("You've been invited",
			para("You've been invited to collaborate in a crmkit workspace.")+
				para(fmt.Sprintf("To accept, sign in using <strong>this email address</strong> (%s) - you'll join the workspace automatically.", to))+
				button("Sign in to crmkit", baseURL)+
				fmt.Sprintf(`<p style="font-size:12px;color:#9a9a9a;margin:16px 0 0;word-break:break-all;">%s</p>`, baseURL)),
	}
}
