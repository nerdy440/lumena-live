// Legal pages (Privacy Policy, Terms of Service) served as real, live,
// publicly-reachable HTML at /privacy-policy and /terms-of-service —
// required by Google Play Console's Data Safety / store listing, and
// linked from the mobile app's Settings screen instead of the placeholder
// alerts it shipped with.
//
// IMPORTANT: this is a genuine starting draft, not a substitute for
// review by a qualified lawyer licensed in the jurisdictions this app
// actually operates in. It covers the real mechanics this codebase
// implements (account deletion, age status, virtual currency, content
// moderation) rather than generic boilerplate, but "matches the code" is
// not the same as "compliant" — treat it as the thing to hand a lawyer
// for review, not the final artifact.
package main

import (
	"net/http"
)

const legalPageStyle = `
  :root {
    --c-bg: #0a0a0f; --c-surface: #15151f; --c-surface2: #1e1e2a; --c-border: #2a2a38;
    --c-text: #f2f2f5; --c-text2: #a8a8b8; --c-text3: #6b6b7d;
    --c-primary: #6c47ff; --c-primary2: #9b72ff; --c-accent: #f59e0b;
  }
  * { box-sizing: border-box; }
  body {
    background: var(--c-bg); color: var(--c-text);
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    max-width: 760px; margin: 0 auto; padding: 32px 20px 80px; line-height: 1.6;
  }
  a { color: var(--c-primary2); }
  h1 { font-size: 26px; margin-bottom: 4px; }
  h2 { font-size: 18px; margin-top: 36px; color: var(--c-primary2); }
  .updated { color: var(--c-text3); font-size: 13px; margin-bottom: 24px; }
  .draft-notice {
    background: var(--c-accent)22; border: 1px solid var(--c-accent)55; border-radius: 10px;
    padding: 14px 16px; font-size: 13px; color: var(--c-text2); margin-bottom: 28px;
  }
  ul { padding-left: 20px; } li { margin-bottom: 6px; }
  .back { display: inline-block; margin-bottom: 20px; color: var(--c-text3); font-size: 13px; text-decoration: none; }
`

func servePrivacyPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Privacy Policy — Lumena Live</title><style>` + legalPageStyle + `</style></head><body>
<a class="back" href="/">← Back to Lumena Live</a>
<h1>Privacy Policy</h1>
<p class="updated">Last updated: this is a live document served directly by the app — see the "draft" notice below.</p>
<div class="draft-notice">
  <strong>Draft notice:</strong> this policy was written to accurately describe what Lumena Live's code actually
  does today. It has not been reviewed by a lawyer and is not a substitute for one — before this app is used by
  real people outside of testing, have it reviewed by counsel familiar with the jurisdictions you operate in
  (data protection law varies significantly by country, e.g. GDPR in the EU, and requirements for apps handling
  live video and virtual currency add further obligations in some regions).
</div>

<h2>1. Who we are</h2>
<p>Lumena Live ("we", "us", "the app") is a live-streaming and social platform. This policy explains what
information we collect when you use the app, why, and what control you have over it.</p>

<h2>2. Information we collect</h2>
<ul>
  <li><strong>Account information:</strong> email address (or phone number), a password hash (never the
  password itself), and the region you registered from.</li>
  <li><strong>Profile information:</strong> display name, bio, avatar, and any interests or languages you add.
  This is visible to other users.</li>
  <li><strong>Content you create:</strong> chat messages, gifts sent/received, stream titles and metadata,
  reports you file, and match/referral activity.</li>
  <li><strong>Payment information:</strong> we never see or store your card number. Purchases are processed
  entirely by Google Play Billing (Android) or Stripe (web) — we only receive confirmation that a purchase
  succeeded and its amount.</li>
  <li><strong>Age information:</strong> a self-declared date of birth, and (if you complete a formal
  verification step) an assurance status from that process. This app requires all users to be 18 or older.</li>
  <li><strong>Device and log information:</strong> IP address, device type, and basic request logs, used for
  security, abuse prevention, and debugging.</li>
</ul>

<h2>3. How we use your information</h2>
<ul>
  <li>To provide the core service: your feed, live streams, chat, matching, gifting, and wallet.</li>
  <li>To keep the platform safe: content moderation, fraud/chargeback detection, and enforcing our
  <a href="/terms-of-service">Terms of Service</a>.</li>
  <li>To communicate with you about your account (e.g. security notices).</li>
  <li>To comply with legal obligations where applicable.</li>
</ul>
<p>We do not sell your personal information to third parties, and we do not use your private chat content for
advertising.</p>

<h2>4. Who we share information with</h2>
<ul>
  <li><strong>Infrastructure providers</strong> that host our database and servers, solely to run the service.</li>
  <li><strong>Payment processors</strong> (Google Play Billing, Stripe) to process purchases.</li>
  <li><strong>Other users</strong> — your display name, avatar, bio, and any public activity (streams, comments)
  are visible to other users by design; private messages are visible only to their participants.</li>
  <li><strong>Law enforcement or regulators</strong>, only when legally required (e.g. a valid subpoena) or to
  protect the safety of a user.</li>
</ul>

<h2>5. How long we keep your information</h2>
<p>We retain your account data while your account is active. If you request deletion, your account enters a
30-day grace period during which you can cancel the deletion; after that period, your account and associated
data are permanently deleted. Some records (e.g. transaction history) may be retained longer where required
for fraud prevention or legal/tax obligations.</p>

<h2>6. Your rights and choices</h2>
<ul>
  <li><strong>Access and correction:</strong> you can view and edit your profile information directly in the
  app at any time.</li>
  <li><strong>Deletion:</strong> you can request account deletion from Settings → Account Settings. This starts
  a 30-day grace period, cancellable at any time within it.</li>
  <li><strong>Blocking:</strong> you can block any account, which prevents them from contacting or following
  you; manage this from Settings → Blocked Accounts.</li>
  <li>Depending on where you live, you may have additional rights (e.g. data portability, objection to
  processing) under laws such as the EU/UK GDPR. Contact us using the details below to exercise them.</li>
</ul>

<h2>7. Children's privacy</h2>
<p>Lumena Live is restricted to users 18 years of age or older. We do not knowingly collect information from
anyone under 18. If we learn that we have collected information from a minor, we will delete it.</p>

<h2>8. Security</h2>
<p>We use industry-standard practices to protect your information, including encrypting data in transit (TLS).
No online service can guarantee absolute security, and we encourage you to use a strong, unique password.</p>

<h2>9. International data transfers</h2>
<p>Our servers may be located in a different country than you are. By using the app, you understand your
information may be processed outside your home country.</p>

<h2>10. Changes to this policy</h2>
<p>We may update this policy as the app changes. Material changes will be reflected here with an updated date.</p>

<h2>11. Contact us</h2>
<p>Questions about this policy or your data can be sent to the contact address listed in the app's store
listing.</p>
</body></html>`))
}

func serveTermsOfService(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Terms of Service — Lumena Live</title><style>` + legalPageStyle + `</style></head><body>
<a class="back" href="/">← Back to Lumena Live</a>
<h1>Terms of Service</h1>
<p class="updated">Last updated: this is a live document served directly by the app — see the "draft" notice below.</p>
<div class="draft-notice">
  <strong>Draft notice:</strong> these terms were written to accurately describe what Lumena Live's code
  actually does today (virtual currency mechanics, moderation, account deletion, etc.). They have not been
  reviewed by a lawyer and are not a substitute for one — have them reviewed by counsel before this app is
  used by real people outside of testing, particularly around virtual-currency regulation and content
  moderation obligations, which vary significantly by country.
</div>

<h2>1. Acceptance of these terms</h2>
<p>By creating an account or using Lumena Live, you agree to these Terms of Service and our
<a href="/privacy-policy">Privacy Policy</a>. If you do not agree, do not use the app.</p>

<h2>2. Eligibility</h2>
<p>You must be at least 18 years old to use Lumena Live. By registering, you confirm you meet this
requirement. We may ask you to verify your age and may restrict or terminate accounts we believe belong to
someone under 18.</p>

<h2>3. Your account</h2>
<p>You are responsible for maintaining the confidentiality of your account credentials and for all activity
under your account. Notify us immediately if you suspect unauthorized access.</p>

<h2>4. Acceptable use</h2>
<p>You agree not to:</p>
<ul>
  <li>Post, stream, or send content that is illegal, that sexually exploits or endangers minors in any way
  (zero tolerance — accounts are permanently terminated and reported to relevant authorities), or that
  harasses, threatens, or targets another person.</li>
  <li>Impersonate another person or misrepresent your identity or age.</li>
  <li>Use the platform for fraud, money laundering, or to circumvent our virtual-currency or payment systems.</li>
  <li>Attempt to interfere with, disrupt, or gain unauthorized access to the service.</li>
  <li>Use automated means (bots, scrapers) to access the service without our written permission.</li>
</ul>
<p>We may remove content, suspend, or permanently terminate accounts that violate these terms, at our
discretion, and you can report other users or content for review at any time from within the app.</p>

<h2>5. Virtual currency (Coins and Diamonds)</h2>
<ul>
  <li>Coins are purchased with real money and used to send gifts and unlock content. Diamonds are earned by
  creators from gifts received and can be exchanged for a payout, subject to our creator payout terms.</li>
  <li>Coins and Diamonds have <strong>no cash value</strong> outside the platform, cannot be exchanged for
  money except through the official creator payout process, and are not a financial investment or security.</li>
  <li>Purchases of Coins are final once delivered to your account, except where required otherwise by law in
  your jurisdiction, or where a purchase fails to deliver due to a verified technical error on our end.</li>
  <li>We may adjust prices, introduce or discontinue virtual items, or reset balances in cases of confirmed
  fraud or a violation of these terms.</li>
  <li>You can set self-imposed purchase limits and a cooling-off period on Coin purchases at any time from
  Wallet → Spend Limits.</li>
</ul>

<h2>6. Live streaming, chat, and video calls</h2>
<p>Lumena Live lets you broadcast live, message other users, and (where enabled) start private video calls.
You are solely responsible for content you broadcast or send. We use a combination of automated tools, user
reports, and human moderation to enforce these terms, but we do not pre-screen all live content — report
anything that violates these terms immediately using the in-app report tools.</p>

<h2>7. Creator payouts</h2>
<p>If you become a creator and accumulate Diamonds, payouts are subject to separate verification (including
identity/KYC checks where required by law), minimum payout thresholds, and processing times disclosed in the
Creator Dashboard. We may withhold or reverse a payout tied to a violation of these terms or to a confirmed
fraudulent transaction.</p>

<h2>8. Content ownership</h2>
<p>You retain ownership of content you create. By posting or streaming on Lumena Live, you grant us a
non-exclusive, worldwide license to host, display, and distribute that content as necessary to operate the
service (e.g. showing your stream to viewers).</p>

<h2>9. Disclaimers</h2>
<p>The service is provided "as is." We do not guarantee it will be uninterrupted, error-free, or that any
particular user's content or behavior meets a standard of quality or safety, though we work to enforce these
terms. Use of live video features with other users carries inherent risk you accept by using the app.</p>

<h2>10. Limitation of liability</h2>
<p>To the maximum extent permitted by law, Lumena Live and its operators are not liable for indirect,
incidental, or consequential damages arising from your use of the service, including content posted by other
users.</p>

<h2>11. Termination</h2>
<p>You may delete your account at any time from Settings → Account Settings, which starts a 30-day grace
period before permanent deletion. We may suspend or terminate your account for violating these terms.</p>

<h2>12. Governing law</h2>
<p>These terms are governed by the laws of the jurisdiction in which the operating entity is registered,
without regard to conflict-of-law principles, except where local consumer-protection law in your country of
residence requires otherwise.</p>

<h2>13. Changes to these terms</h2>
<p>We may update these terms as the app changes. Continuing to use the app after an update means you accept
the revised terms.</p>

<h2>14. Contact us</h2>
<p>Questions about these terms can be sent to the contact address listed in the app's store listing.</p>
</body></html>`))
}
