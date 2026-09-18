// main is the unified Lumena API server — Phases 2, 3, 4.
// Runs auth, profile, and feed on a single port.
// Configuration: environment variables (see envOr calls below).
//
// Quick start:
//   JWT_SECRET="at-least-32-chars-secret-here!!" go run ./cmd/api/main.go
//
// Then open http://localhost:8080 for the web UI,
// or hit http://localhost:8080/api/v1/feed for the JSON API.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/db"

	// Auth
	authHandlers "github.com/lumena/auth/handlers"
	"github.com/lumena/auth/authsvc"
	"github.com/lumena/auth/memrepo"
	authpg "github.com/lumena/auth/pgrepo"
	"github.com/lumena/auth/otp"
	"github.com/lumena/auth/token"

	// Profile
	profileHandlers "github.com/lumena/profile/handlers"
	"github.com/lumena/profile"
	profilepg "github.com/lumena/profile/pgrepo"
	"github.com/lumena/profile/profilesvc"
	"github.com/lumena/profile/social"
	socialpg "github.com/lumena/profile/social/pgrepo"

	// Feed
	feedHandlers "github.com/lumena/feed/handlers"

	// Streaming
	streamHandlers "github.com/lumena/streaming/handlers"
	"github.com/lumena/streaming"
	streamingpg "github.com/lumena/streaming/pgrepo"
	"github.com/lumena/feed"
	"github.com/lumena/feed/feedsvc"
	feedpg "github.com/lumena/feed/pgrepo"

	// Realtime gateway (Phase 6)
	"github.com/lumena/gateway/bus"
	"github.com/lumena/gateway/ws"

	// Private chat (Phase 7)
	"github.com/lumena/chat"
	"github.com/lumena/chat/chatsvc"
	chatHandlers "github.com/lumena/chat/handlers"
	chatpg "github.com/lumena/chat/pgrepo"

	// Attachments (Phase 10)
	"github.com/lumena/chat/attachsvc"
	attachHandlers "github.com/lumena/chat/attachsvc/handlers"

	// Gifts / wallet (Phase 8)
	"github.com/lumena/ledger"
	"github.com/lumena/ledger/giftsvc"
	ledgerHandlers "github.com/lumena/ledger/handlers"
	ledgerpg "github.com/lumena/ledger/pgrepo"

	// Orders / spend limits (Phase 9)
	"github.com/lumena/ledger/ordersvc"
	ordpg "github.com/lumena/ledger/ordpgrepo"
	"github.com/lumena/ledger/reconciler"
	"github.com/lumena/ledger/store"

	// Private video (Phase 11)
	"github.com/lumena/pv"
	pvHandlers "github.com/lumena/pv/handlers"
	pvpg "github.com/lumena/pv/pgrepo"
	"github.com/lumena/pv/pvsvc"

	// Matchmaking (Phase 12)
	"github.com/lumena/match"
	matchHandlers "github.com/lumena/match/handlers"
	"github.com/lumena/match/matchsvc"
	matchpg "github.com/lumena/match/pgrepo"

	// Translation (Phase 13)
	"github.com/lumena/translate"

	// Creator dashboard (Phase 14)
	"github.com/lumena/creator"
	creatorHandlers "github.com/lumena/creator/handlers"
	"github.com/lumena/creator/creatorsvc"
	creatorpg "github.com/lumena/creator/pgrepo"

	// Moderation (Phase 15)
	"github.com/lumena/moderation"
	modHandlers "github.com/lumena/moderation/handlers"
	"github.com/lumena/moderation/modsvc"
	modpg "github.com/lumena/moderation/pgrepo"

	// Admin dashboard (Phase 16)
	adminmod "github.com/lumena/admin"
	"github.com/lumena/admin/adminsvc"
	adminHandlers "github.com/lumena/admin/handlers"
	adminpg "github.com/lumena/admin/pgrepo"

	// Analytics (Phase 17)
	"github.com/lumena/analytics"
	"github.com/lumena/analytics/analyticssvc"
	analyticsHandlers "github.com/lumena/analytics/handlers"
	analyticspg "github.com/lumena/analytics/pgrepo"

	// Fraud prevention (Phase 18)
	fraudmod "github.com/lumena/fraud"
	fraudHandlers "github.com/lumena/fraud/handlers"
	"github.com/lumena/fraud/fraudsvc"
	fraudpg "github.com/lumena/fraud/pgrepo"

	// Feature flags / staged rollout / canary metrics (Phase 20)
	"github.com/lumena/rollout"
	rolloutHandlers "github.com/lumena/rollout/handlers"
	"github.com/lumena/rollout/rolloutsvc"
	rolloutpg "github.com/lumena/rollout/pgrepo"

	// Referral / invite rewards
	"github.com/lumena/referral"
	referralHandlers "github.com/lumena/referral/handlers"
	referralpg "github.com/lumena/referral/pgrepo"
	"github.com/lumena/referral/referralsvc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	port := envOr("PORT", "8080")
	jwtSecret := []byte(envOr("JWT_SECRET", ""))
	if len(jwtSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}

	// ── Database (Phase 1 of the public-launch plan) ─────────────────────────
	// Every module that has a real Postgres repo (auth, ledger, profile,
	// social, chat, match, pv, creator, moderation, admin, fraud, rollout,
	// analytics) switches to it when DATABASE_URL is set; everything else
	// (feed rooms/notifications, streaming sessions, referral, ledger
	// orders/spend-limits, rollout's canary metrics) stays in-memory-only
	// regardless — those modules have no Postgres repo yet. Leaving
	// DATABASE_URL unset keeps the exact all-in-memory dev behavior this
	// server has always had.
	ctx := context.Background()
	var dbPool *pgxpool.Pool
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		var err error
		dbPool, err = db.Connect(ctx, databaseURL)
		if err != nil {
			return fmt.Errorf("connect to database: %w", err)
		}
		defer dbPool.Close()
		if err := db.RunMigrations(ctx, dbPool, "migrations"); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
		logger.Info("persistence: using Postgres (DATABASE_URL set)")
	} else {
		logger.Warn("persistence: DATABASE_URL not set — using in-memory storage (NEVER for production, all data lost on restart)")
	}

	// ── Auth ──────────────────────────────────────────────────────────────────
	issuer, err := token.NewIssuer(jwtSecret, "lumena-api", 15*time.Minute)
	if err != nil {
		return fmt.Errorf("issuer: %w", err)
	}
	var authRepo authsvc.Repo
	if dbPool != nil {
		authRepo = authpg.New(dbPool)
	} else {
		authRepo = memrepo.New()
	}
	otpSvc := otp.NewService(otp.NewMemoryStore(), &otp.LogSender{})
	authService := authsvc.NewService(authRepo, otpSvc, issuer)
	authH := authHandlers.New(authService, issuer)

	// ── Profile ───────────────────────────────────────────────────────────────
	var profileRepo profile.Repo
	var privacyRepo profile.PrivacyRepo
	var socialRepo social.Repo
	if dbPool != nil {
		profileRepo = profilepg.New(dbPool)
		privacyRepo = profilepg.NewPrivacyRepo(dbPool)
		socialRepo = socialpg.New(dbPool)
	} else {
		profileRepo = profile.NewMemProfileRepo()
		privacyRepo = profile.NewMemPrivacyRepo()
		socialRepo = social.NewMemSocialRepo()
	}
	profileService := profilesvc.NewService(profileRepo, privacyRepo, socialRepo)
	profileH := profileHandlers.New(profileService)

	// ── Feed ──────────────────────────────────────────────────────────────────
	var roomRepo feed.RoomRepo
	var notifRepo feed.NotificationRepo
	if dbPool != nil {
		roomRepo = feedpg.NewRoomRepo(dbPool)
		notifRepo = feedpg.NewNotificationRepo(dbPool)
	} else {
		roomRepo = feed.NewMemRoomRepo()
		notifRepo = feed.NewMemNotificationRepo()
	}
	engine := feed.NewRuleBasedEngine(feed.DefaultWeights())
	feedService := feedsvc.NewService(roomRepo, notifRepo, engine)

	// ── Streaming ────────────────────────────────────────────────────────────
	// streamRepoFull extends streaming.SessionRepo with ListByHost/ListAll —
	// real methods on both *streaming.MemSessionRepo and streamingpg.Repo,
	// deliberately outside SessionRepo itself (see mem_impl.go's doc
	// comments) but needed directly by the creator dashboard and analytics
	// closures below.
	type streamRepoFull interface {
		streaming.SessionRepo
		ListByHost(ctx context.Context, hostID string) ([]streaming.StreamSession, error)
		ListAll(ctx context.Context) ([]streaming.StreamSession, error)
	}
	var streamRepo streamRepoFull
	if dbPool != nil {
		streamRepo = streamingpg.New(dbPool)
	} else {
		streamRepo = streaming.NewMemSessionRepo()
	}
	streamIngest := streaming.NewDevIngestService()
	streamPackager := &streaming.DevPackagerService{}
	streamClassifier := streaming.NewDevModerationClassifier()
	streamService := streaming.NewBroadcastService(streamRepo, streamIngest, streamPackager, streamClassifier)
	streamH := streamHandlers.New(streamService)

	// ── Private chat (Phase 7) ───────────────────────────────────────────────
	// isBlocked/isMutual are thin adapters over the existing social graph
	// (profile/social) so the chat module never has to import profile —
	// same DI pattern as the realtime gateway's IsHostFunc.
	isBlocked := func(ctx context.Context, a, b string) (bool, error) {
		rel, err := profileService.GetRelationship(ctx, a, b)
		if err != nil {
			return false, err
		}
		return rel.Blocked || rel.BlockedBy, nil
	}
	isMutual := func(ctx context.Context, a, b string) (bool, error) {
		rel, err := profileService.GetRelationship(ctx, a, b)
		if err != nil {
			return false, err
		}
		return rel.Following && rel.FollowedBy, nil
	}
	var chatRepo chat.Repo
	if dbPool != nil {
		chatRepo = chatpg.New(dbPool)
	} else {
		chatRepo = chat.NewMemChatRepo()
	}
	chatService := chatsvc.NewService(chatRepo, isBlocked, isMutual)

	// ── Attachments (Phase 10) ───────────────────────────────────────────────
	attachService := attachsvc.NewService()
	chatService = chatService.WithAttachments(attachService.IsDeliverable)
	chatH := chatHandlers.New(chatService)
	attachH := attachHandlers.New(attachService)

	// ── Gifts / wallet (Phase 8) ─────────────────────────────────────────────
	// ledgerRepoFull extends ledger.Repo with ListByKind/IntegrityCheck —
	// both real methods on *ledger.MemLedger and ledgerpg.Repo, but
	// deliberately outside the Repo interface itself (see mem_ledger.go's
	// ListByKind doc comment: read-only economy-scan surface, never a
	// second way to mutate the ledger). admin/analytics/fraud's closures
	// below and the integrity-check ticker need them directly.
	type ledgerRepoFull interface {
		ledger.Repo
		ListByKind(ctx context.Context, kind string, since time.Time) ([]ledger.Transaction, error)
		IntegrityCheck() map[ledger.Currency]int64
	}
	var ledgerRepo ledgerRepoFull
	if dbPool != nil {
		ledgerRepo = ledgerpg.New(dbPool)
	} else {
		ledgerRepo = ledger.NewMemLedger()
	}
	giftService := giftsvc.NewService(ledgerRepo)

	// Premium/private room unlocks — same coin -> platform-take -> diamond
	// economics as a gift send (giftsvc.SendGift), just charged once per
	// (room, viewer) instead of per gift. platformTakeBPS/diamondsPerThousandCoins
	// mirror the unexported constants in ledger/giftsvc/service.go exactly.
	const (
		premiumPlatformTakeBPS         = 3000
		premiumDiamondsPerThousandCoins = 700
	)
	var premiumUnlockRepo feed.PremiumUnlockRepo
	if dbPool != nil {
		premiumUnlockRepo = feedpg.NewPremiumUnlockRepo(dbPool)
	} else {
		premiumUnlockRepo = feed.NewMemPremiumUnlockRepo()
	}
	unlockRoomCoins := func(ctx context.Context, viewerID, hostID string, priceCoins int64, idempotencyKey string) (string, error) {
		platformShare := priceCoins * premiumPlatformTakeBPS / 10000
		hostCoinsEquivalent := priceCoins - platformShare
		hostDiamonds := hostCoinsEquivalent * premiumDiamondsPerThousandCoins / 1000
		entries := []ledger.Entry{
			{AccountID: ledger.UserCoinsAccount(viewerID), Amount: -priceCoins, Currency: ledger.Coin},
			{AccountID: ledger.PlatformRevenueAccount, Amount: platformShare, Currency: ledger.Coin},
			{AccountID: ledger.PlatformLiabilityAccount, Amount: hostCoinsEquivalent, Currency: ledger.Coin},
			{AccountID: ledger.UserDiamondsAccount(hostID), Amount: hostDiamonds, Currency: ledger.Diamond},
			{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -hostDiamonds, Currency: ledger.Diamond},
		}
		metadata := map[string]any{"room_host_id": hostID, "viewer_id": viewerID, "price_coins": priceCoins}
		tx, _, err := ledgerRepo.PostTransaction(ctx, "premium_unlock", idempotencyKey, metadata, entries)
		if err != nil {
			return "", err
		}
		return tx.ID, nil
	}
	feedService = feedService.WithPremiumRooms(premiumUnlockRepo, unlockRoomCoins)
	feedH := feedHandlers.New(feedService)

	// ── Referral / invite rewards ────────────────────────────────────────────
	// A referral code is just an account ID — no separate code-generation or
	// lookup table needed. accountExists and creditCoins are the two real
	// systems (auth, ledger) this package needs, injected as closures so it
	// never imports either directly, same DI pattern as every other module.
	var referralRepo referral.Repo
	if dbPool != nil {
		referralRepo = referralpg.New(dbPool)
	} else {
		referralRepo = referral.NewMemRepo()
	}
	referralAccountExists := func(ctx context.Context, accountID string) (bool, error) {
		acc, err := authRepo.FindByID(ctx, accountID)
		if err != nil {
			return false, err
		}
		return acc != nil, nil
	}
	referralCreditCoins := func(ctx context.Context, accountID string, amount int64, idempotencyKey string) (string, error) {
		entries := []ledger.Entry{
			{AccountID: ledger.UserCoinsAccount(accountID), Amount: amount, Currency: ledger.Coin},
			{AccountID: ledger.PlatformLiabilityAccount, Amount: -amount, Currency: ledger.Coin},
		}
		tx, _, err := ledgerRepo.PostTransaction(ctx, "referral_reward", idempotencyKey, map[string]any{"account_id": accountID}, entries)
		if err != nil {
			return "", err
		}
		return tx.ID, nil
	}
	referralService := referralsvc.New(referralRepo, referralAccountExists, referralCreditCoins)
	referralH := referralHandlers.New(referralService)

	// ── Local persistence ────────────────────────────────────────────────────
	// See localstate.go's doc comment: a lightweight JSON-snapshot stand-in
	// for a real database, used only when running on the in-memory repos —
	// once DATABASE_URL is set, Postgres itself is the persistence and this
	// whole mechanism is redundant, so it's skipped entirely in that mode
	// (the type assertions below simply fail to match, since ledgerpg.Repo
	// etc. have no Snapshot/Restore methods). Disabled either way by
	// leaving LOCAL_STATE_FILE unset.
	var localRepos localStateRepos
	if dbPool == nil {
		localRepos.Auth, _ = authRepo.(restorerSnapshotter)
		localRepos.Profile, _ = profileRepo.(restorerSnapshotter)
		localRepos.Privacy, _ = privacyRepo.(restorerSnapshotter)
		localRepos.Social, _ = socialRepo.(restorerSnapshotter)
		localRepos.Ledger, _ = ledgerRepo.(restorerSnapshotter)
	}
	statePath := os.Getenv("LOCAL_STATE_FILE")
	if statePath != "" && dbPool == nil {
		loadLocalState(logger, statePath, localRepos)
	}

	// ── Orders / spend limits (Phase 9) ──────────────────────────────────────
	var orderRepo ledger.OrderRepo
	var spendLimitsRepo ledger.SpendLimitsRepo
	if dbPool != nil {
		orderRepo = ordpg.NewOrderRepo(dbPool)
		spendLimitsRepo = ordpg.NewSpendLimitsRepo(dbPool)
	} else {
		orderRepo = ledger.NewMemOrderRepo()
		spendLimitsRepo = ledger.NewMemSpendLimitsRepo()
	}
	// Real purchase verification requires a real vendor account per
	// platform — each is wired in only when its own env var is actually
	// set, and every platform without one falls back to DevVerifier under
	// that platform key, exactly as the single-verifier version always
	// did. This is not a silent fake-prod switch, it's the same dev-only
	// stand-in every other DevXxx in this codebase uses until its real
	// vendor dependency is available. dev_store always maps to DevVerifier
	// regardless — it's the client-selectable "definitely not a real
	// store" platform, used by web/dev builds and tests.
	//
	// google_play is Play Store policy, not a preference: any digital
	// good "consumed within the app" (coins/diamonds) must go through
	// Google Play Billing on Android, not Stripe — see
	// store.GooglePlayVerifier's doc comment.
	verifiers := map[string]store.Verifier{
		ledger.PlatformDevStore: store.NewDevVerifier(),
	}
	if stripeKey := os.Getenv("STRIPE_SECRET_KEY"); stripeKey != "" {
		verifiers[ledger.PlatformStripe] = store.NewStripeVerifier(stripeKey)
		slog.Info("purchase verification: stripe platform using StripeVerifier (real Stripe API calls)")
	} else {
		verifiers[ledger.PlatformStripe] = store.NewDevVerifier()
		slog.Warn("purchase verification: STRIPE_SECRET_KEY not set — stripe platform using DevVerifier (NEVER for production)")
	}
	if saJSON := os.Getenv("GOOGLE_PLAY_SERVICE_ACCOUNT_JSON"); saJSON != "" {
		packageName := os.Getenv("GOOGLE_PLAY_PACKAGE_NAME")
		gpv, err := store.NewGooglePlayVerifier([]byte(saJSON), packageName)
		if err != nil {
			return fmt.Errorf("parse GOOGLE_PLAY_SERVICE_ACCOUNT_JSON: %w", err)
		}
		verifiers[ledger.PlatformGooglePlay] = gpv
		slog.Info("purchase verification: google_play platform using GooglePlayVerifier (real Play Developer API calls)", "package", packageName)
	} else {
		verifiers[ledger.PlatformGooglePlay] = store.NewDevVerifier()
		slog.Warn("purchase verification: GOOGLE_PLAY_SERVICE_ACCOUNT_JSON not set — google_play platform using DevVerifier (NEVER for production; Play Store requires Play Billing for real releases)")
	}
	purchaseVerifier := store.NewMultiVerifier(verifiers)
	orderService := ordersvc.NewService(orderRepo, ledgerRepo, purchaseVerifier).
		WithSpendLimits(spendLimitsRepo)
	giftH := ledgerHandlers.New(giftService, ledgerRepo).WithOrders(orderService, spendLimitsRepo)

	// Reconciler: retries orders stuck in a non-terminal state so a crash
	// between store confirmation and ledger credit self-heals (doc 06 §10,
	// roadmap Phase 9 exit gate). 5s here for a responsive dev/demo loop;
	// production would run this every ~30s per the doc.
	orderReconciler := reconciler.New(orderService, 5*time.Second, logger)
	orderReconciler.Start(context.Background())
	defer orderReconciler.Stop()

	// Nightly ledger integrity check (doc 06 §9 invariant 2) — logged, not
	// enforced, since a violation here means a bug already happened; this
	// is the alarm, not the guard rail. Runs hourly in this dev build
	// rather than nightly so the check actually fires during a demo/test run.
	integrityTicker := time.NewTicker(time.Hour)
	go func() {
		for range integrityTicker.C {
			for currency, sum := range ledgerRepo.IntegrityCheck() {
				if sum != 0 {
					logger.Error("ledger integrity check FAILED", "currency", currency, "sum", sum)
				}
			}
		}
	}()
	defer integrityTicker.Stop()

	// ── Realtime gateway (Phase 6) ───────────────────────────────────────────
	// In-process event bus standing in for the Kafka fan-out bus of doc 08 §12
	// — see gateway/bus's doc comment. Room topic is "room:{id}" (doc 08 §2).
	realtimeBus := bus.New()
	wsTokens := ws.NewTokenStore()
	isHost := func(ctx context.Context, roomID, accountID string) (bool, error) {
		room, err := feedService.GetRoom(ctx, roomID)
		if err != nil {
			return false, err
		}
		return room.HostID == accountID, nil
	}
	giftService.WithRoomHostCheck(isHost)
	sendPrivateMessage := func(ctx context.Context, senderID, conversationID, clientMsgID, body, attachmentID string) (map[string]any, error) {
		msg, _, err := chatService.SendMessage(ctx, senderID, conversationID, clientMsgID, body, attachmentID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"msg_id": msg.ID, "sender_id": msg.SenderID, "body": msg.Body,
			"attachment_id": msg.AttachmentID,
			"seq": msg.Seq, "delivered_at": msg.DeliveredAt,
		}, nil
	}
	markConvRead := func(ctx context.Context, viewerID, conversationID string, upToSeq uint64) error {
		return chatService.MarkRead(ctx, viewerID, conversationID, upToSeq)
	}
	sendGift := func(ctx context.Context, senderID, recipientID, roomID, giftID string, quantity int64, idempotencyKey string) (map[string]any, error) {
		result, err := giftService.SendGift(ctx, senderID, recipientID, roomID, giftID, quantity, idempotencyKey)
		if err != nil {
			return nil, err
		}
		_, recipientDiamonds, err := giftService.Balance(ctx, recipientID)
		if err != nil {
			return nil, err
		}
		giftName, giftAssetURL := "", ""
		for _, g := range giftService.Catalogue() {
			if g.ID == result.GiftID {
				giftName, giftAssetURL = g.Name, g.AssetURL
				break
			}
		}
		return map[string]any{
			"transaction_id": result.TransactionID, "gift_id": result.GiftID, "quantity": result.Quantity,
			"gift_name": giftName, "gift_asset_url": giftAssetURL,
			"coins_spent": result.CoinsSpent, "new_sender_balance": result.NewSenderBalance,
			"creator_diamonds": result.CreatorDiamonds, "new_recipient_diamonds": recipientDiamonds,
		}, nil
	}
	// ageAssured is shared by matchmaking (adult-only matching) and the
	// moderation risk engine (adult-proxy signal) — it outlived the private
	// video feature it was originally written for, so it keeps its own name
	// here rather than either module's.
	ageAssured := func(ctx context.Context, accountID string) (bool, error) {
		// authRepo.FindByID's Account.AgeStatus field is a stale snapshot —
		// DeclareAge/SetAgeStatus write to a separate map that only
		// authService.GetAgeStatus reads correctly. See that method.
		status, err := authService.GetAgeStatus(ctx, accountID)
		if err != nil {
			return false, err
		}
		return status == authsvc.AgeAssured, nil
	}

	// ── Private video (Phase 11) ─────────────────────────────────────────────
	// See pv package's doc comment: this is the gate chain, billing hold, and
	// interval settlement — no real WebRTC SFU/TURN media server, and no
	// real CSAM frame-sampling (doc 09 §5's PV-07, a vendor/legal dependency
	// excluded here for the same reason it was excluded from Phase 6).
	pvAccountActive := func(ctx context.Context, accountID string) (bool, error) {
		acc, err := authRepo.FindByID(ctx, accountID)
		if err != nil || acc == nil {
			return false, err
		}
		return acc.Status == authsvc.StatusActive, nil
	}
	pvCalleePermits := func(ctx context.Context, callerID, calleeID string) (bool, error) {
		privacy, err := profileService.GetPrivacySettings(ctx, calleeID)
		if err != nil {
			return false, err
		}
		switch privacy.WhoCanCall {
		case profile.PolicyEveryone:
			return true, nil
		case profile.PolicyNobody:
			return false, nil
		default: // PolicyMutuals
			return isMutual(ctx, callerID, calleeID)
		}
	}
	pvPublish := func(topic, eventType string, payload map[string]any) {
		realtimeBus.Publish(topic, eventType, payload)
	}
	// No WithPrivateVideo/SDP-relay wiring here — unlike the reference
	// app's browser client, the mobile app has no camera/WebRTC stack to
	// negotiate real media with (see pv package's doc comment), so there's
	// no signaling to relay yet. Session lifecycle and billing below are
	// entirely REST-driven (request/accept/decline/end) and settle on
	// wall-clock elapsed time between AcceptedAt and EndedAt.
	var pvRepo pv.Repo
	if dbPool != nil {
		pvRepo = pvpg.New(dbPool)
	} else {
		pvRepo = pv.NewMemRepo()
	}
	pvService := pvsvc.NewService(pvRepo, ledgerRepo, pvAccountActive, ageAssured, isBlocked, pvCalleePermits, pvPublish)
	pvH := pvHandlers.New(pvService)

	// ── Translation (Phase 13) ───────────────────────────────────────────────
	// translate.New() resolves at compile time to DevProvider unless this
	// binary is built with -tags production, in which case it resolves to
	// the unconfigured ProductionProvider stub instead — see the translate
	// package's doc comments.
	translationProvider := translate.New()
	translateFn := func(ctx context.Context, body, sourceLang, targetLang string) (string, float64, error) {
		result, err := translationProvider.Translate(ctx, body, sourceLang, targetLang)
		if err != nil {
			return "", 0, err
		}
		return result.TranslatedBody, result.Confidence, nil
	}

	wsHandler := ws.NewHandler(realtimeBus, wsTokens, isHost, logger).
		WithPrivateChat(chatService.IsParticipant, sendPrivateMessage, markConvRead).
		WithGifts(sendGift).
		WithTranslation(translateFn).
		WithViewerCount(func(ctx context.Context, roomID string, count int) {
			_ = feedService.UpdateViewerCount(ctx, roomID, count)
		})

	// ── Matchmaking (Phase 12) ───────────────────────────────────────────────
	isMatchable := func(ctx context.Context, accountID string) (bool, error) {
		privacy, err := profileService.GetPrivacySettings(ctx, accountID)
		if err != nil {
			return false, err
		}
		return privacy.Matchable, nil
	}
	var matchRepo match.Repo
	var matchPrefsRepo match.PreferencesRepo
	if dbPool != nil {
		matchRepo = matchpg.New(dbPool)
		matchPrefsRepo = matchpg.NewPreferencesRepo(dbPool)
	} else {
		matchRepo = match.NewMemRepo()
		matchPrefsRepo = match.NewMemPreferencesRepo()
	}
	matchService := matchsvc.NewService(matchRepo, isMatchable, ageAssured, isBlocked)
	matchH := matchHandlers.New(matchService, matchPrefsRepo)

	// ── Creator dashboard (Phase 14) ─────────────────────────────────────────
	// Earnings/payout closures adapt the real ledger and auth services into
	// creatorsvc's DI contract — creator never imports ledger or auth
	// directly, same pattern as match's isMatchable/isBlocked above.
	earningsHistory := func(ctx context.Context, accountID string) ([]creator.EarningEntry, error) {
		acct := ledger.UserDiamondsAccount(accountID)
		var out []creator.EarningEntry
		cursor := ""
		for {
			items, next, err := ledgerRepo.History(ctx, acct, cursor, 200)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				entry := creator.EarningEntry{
					TransactionID:  it.TransactionID,
					Kind:           it.Kind,
					AmountDiamonds: it.Amount,
					CreatedAt:      it.CreatedAt,
				}
				if giftID, ok := it.Metadata["gift_id"].(string); ok {
					entry.GiftID = giftID
				}
				out = append(out, entry)
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return out, nil
	}
	diamondBalance := func(ctx context.Context, accountID string) (int64, error) {
		return ledgerRepo.Balance(ctx, ledger.UserDiamondsAccount(accountID), ledger.Diamond)
	}
	accountRestricted := func(ctx context.Context, accountID string) (bool, error) {
		status, err := authService.GetAccountStatus(ctx, accountID)
		if err != nil {
			return false, err
		}
		return status == authsvc.StatusRestricted || status == authsvc.StatusSuspended, nil
	}
	postPayout := func(ctx context.Context, accountID string, amount int64, idempotencyKey string) (string, error) {
		entries := []ledger.Entry{
			{AccountID: ledger.UserDiamondsAccount(accountID), Amount: -amount, Currency: ledger.Diamond},
			{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: amount, Currency: ledger.Diamond},
		}
		tx, _, err := ledgerRepo.PostTransaction(ctx, "payout", idempotencyKey, map[string]any{"account_id": accountID}, entries)
		if err != nil {
			return "", err
		}
		return tx.ID, nil
	}
	// reversePayout is postPayout's mirror image — used when an admin
	// rejects a payout or a processing payout fails, to return the held
	// diamonds to the creator's available balance via its own ledger
	// transaction rather than editing any balance directly (doc rule 19).
	reversePayout := func(ctx context.Context, accountID string, amount int64, idempotencyKey string) (string, error) {
		entries := []ledger.Entry{
			{AccountID: ledger.UserDiamondsAccount(accountID), Amount: amount, Currency: ledger.Diamond},
			{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -amount, Currency: ledger.Diamond},
		}
		tx, _, err := ledgerRepo.PostTransaction(ctx, "payout_reversal", idempotencyKey, map[string]any{"account_id": accountID}, entries)
		if err != nil {
			return "", err
		}
		return tx.ID, nil
	}
	listHostSessions := func(ctx context.Context, hostID string) ([]creator.StreamStat, error) {
		sessions, err := streamRepo.ListByHost(ctx, hostID)
		if err != nil {
			return nil, err
		}
		out := make([]creator.StreamStat, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, creator.StreamStat{
				SessionID: s.ID, RoomID: s.RoomID, State: string(s.State),
				StartedAt: s.StartedAt, EndedAt: s.EndedAt, PeakViewers: s.PeakViewers,
			})
		}
		return out, nil
	}
	followerHistory := func(ctx context.Context, accountID string) ([]creator.FollowerPoint, error) {
		var out []creator.FollowerPoint
		cursor := ""
		for {
			items, next, err := socialRepo.GetFollowers(ctx, accountID, cursor, 200)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				out = append(out, creator.FollowerPoint{AccountID: it.AccountID, FollowedAt: it.FollowedAt})
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return out, nil
	}
	// payoutRepoFull extends creatorsvc.PayoutRepo with CountPending — a
	// real method on both *creator.MemPayoutRepo and creatorpg.PayoutRepo,
	// deliberately outside the service interface (it's the admin
	// platform-health dashboard's own read, not something creatorsvc
	// itself needs — see MemPayoutRepo.CountPending's doc comment).
	type payoutRepoFull interface {
		creatorsvc.PayoutRepo
		CountPending(ctx context.Context) (int, error)
	}
	var kycRepo creatorsvc.KYCRepo
	var payoutRepo payoutRepoFull
	var payoutProfileRepo creatorsvc.PayoutProfileRepo
	if dbPool != nil {
		kycRepo = creatorpg.NewKYCRepo(dbPool)
		payoutRepo = creatorpg.NewPayoutRepo(dbPool)
		payoutProfileRepo = creatorpg.NewPayoutProfileRepo(dbPool)
	} else {
		kycRepo = creator.NewMemKYCRepo()
		payoutRepo = creator.NewMemPayoutRepo()
		payoutProfileRepo = creator.NewMemPayoutProfileRepo()
	}
	creatorService := creatorsvc.New(
		kycRepo, payoutRepo, payoutProfileRepo,
		earningsHistory, diamondBalance, accountRestricted, postPayout, reversePayout,
		listHostSessions, followerHistory,
	)
	creatorH := creatorHandlers.New(creatorService)

	// ── Moderation (Phase 15) ────────────────────────────────────────────────
	applyEnforcement := func(ctx context.Context, accountID string, action moderation.EnforcementAction) error {
		switch action {
		case moderation.ActionRestrictBroadcast, moderation.ActionRestrictPV:
			// Doc 06's account_status enum has no per-surface restriction —
			// "restricted" is the closest real status, matching how the
			// creator payout gate already reads it.
			return authService.SetAccountStatus(ctx, accountID, authsvc.StatusRestricted)
		case moderation.ActionSuspend, moderation.ActionTerminate:
			return authService.SetAccountStatus(ctx, accountID, authsvc.StatusSuspended)
		default: // warn, mute — no account-wide status change
			return nil
		}
	}
	reinstate := func(ctx context.Context, accountID string) error {
		return authService.SetAccountStatus(ctx, accountID, authsvc.StatusActive)
	}
	riskFollowerCount := func(ctx context.Context, accountID string) (int, error) {
		items, _, err := socialRepo.GetFollowers(ctx, accountID, "", 200)
		if err != nil {
			return 0, err
		}
		return len(items), nil
	}
	riskIsFollowing := func(ctx context.Context, followerID, followeeID string) (bool, error) {
		rel, err := socialRepo.GetRelationship(ctx, followerID, followeeID)
		if err != nil {
			return false, err
		}
		return rel.Following, nil
	}
	var riskRepo modsvc.RiskRepo
	var reportRepo modsvc.ReportRepo
	var enforcementRepo modsvc.EnforcementRepo
	var appealRepo modsvc.AppealRepo
	var moderatorRepo modsvc.ModeratorRepo
	if dbPool != nil {
		riskRepo = modpg.NewRiskRepo(dbPool)
		reportRepo = modpg.NewReportRepo(dbPool)
		enforcementRepo = modpg.NewEnforcementRepo(dbPool)
		appealRepo = modpg.NewAppealRepo(dbPool)
		moderatorRepo = modpg.NewModeratorRepo(dbPool)
	} else {
		riskRepo = moderation.NewMemRiskRepo()
		reportRepo = moderation.NewMemReportRepo()
		enforcementRepo = moderation.NewMemEnforcementRepo()
		appealRepo = moderation.NewMemAppealRepo()
		moderatorRepo = moderation.NewMemModeratorRepo()
	}
	riskEngine := modsvc.NewRiskEngine(
		riskRepo,
		ageAssured, // adult proxy: age-assured (same closure match already uses)
		authService.GetAccountCreatedAt,
		riskFollowerCount,
		riskIsFollowing,
	)
	modService := modsvc.New(
		reportRepo, enforcementRepo, appealRepo,
		moderatorRepo, riskEngine, applyEnforcement, reinstate,
	)
	modH := modHandlers.New(modService)

	// Feed the risk engine from real chat/PV traffic without those modules
	// importing moderation (Phase 15's DI convention).
	chatService.WithMessageHook(func(ctx context.Context, senderID, recipientID, body string) {
		modService.RecordMessage(ctx, senderID, recipientID, body)
	})
	pvService.WithRequestedHook(func(ctx context.Context, callerID, calleeID string) {
		modService.RecordPVRequest(ctx, callerID, calleeID)
	})
	streamService.WithAutoTerminationHook(func(ctx context.Context, hostID, sessionID, ruleID string) {
		modService.RecordAutoTermination(ctx, hostID, sessionID, ruleID)
	})

	// ── Admin dashboard (Phase 16) ───────────────────────────────────────────
	adminGetAccount := func(ctx context.Context, accountID string) (*adminmod.AccountView, error) {
		status, err := authService.GetAccountStatus(ctx, accountID)
		if err != nil {
			return nil, err
		}
		ageStatus, err := authService.GetAgeStatus(ctx, accountID)
		if err != nil {
			return nil, err
		}
		createdAt, err := authService.GetAccountCreatedAt(ctx, accountID)
		if err != nil {
			return nil, err
		}
		return &adminmod.AccountView{AccountID: accountID, Status: string(status), AgeStatus: string(ageStatus), CreatedAt: createdAt}, nil
	}
	adminSetAccountStatus := func(ctx context.Context, accountID, status string) error {
		switch status {
		case "restricted":
			return authService.SetAccountStatus(ctx, accountID, authsvc.StatusRestricted)
		case "active":
			return authService.SetAccountStatus(ctx, accountID, authsvc.StatusActive)
		default:
			return fmt.Errorf("admin: unknown status %q", status)
		}
	}
	adminListGifts := func(ctx context.Context, since time.Time) ([]adminsvc.GiftFlow, error) {
		txs, err := ledgerRepo.ListByKind(ctx, "gift", since)
		if err != nil {
			return nil, err
		}
		out := make([]adminsvc.GiftFlow, 0, len(txs))
		for _, tx := range txs {
			sender, _ := tx.Metadata["sender_id"].(string)
			recipient, _ := tx.Metadata["recipient_id"].(string)
			if sender == "" || recipient == "" {
				continue
			}
			out = append(out, adminsvc.GiftFlow{SenderID: sender, RecipientID: recipient, CreatedAt: tx.CreatedAt})
		}
		return out, nil
	}
	adminLedgerIntegrity := func(ctx context.Context) (map[string]int64, error) {
		sums := ledgerRepo.IntegrityCheck()
		out := make(map[string]int64, len(sums))
		for currency, sum := range sums {
			out[string(currency)] = sum
		}
		return out, nil
	}
	adminActiveStreams := func(ctx context.Context) (int, error) {
		active, err := streamRepo.GetActive(ctx)
		if err != nil {
			return 0, err
		}
		return len(active), nil
	}
	adminOpenReports := func(ctx context.Context) (int, error) {
		items, err := reportRepo.List(ctx, moderation.ReportOpen)
		if err != nil {
			return 0, err
		}
		return len(items), nil
	}
	adminFlaggedRisk := func(ctx context.Context) ([]adminmod.RiskFlag, error) {
		flagged, err := riskRepo.ListFlagged(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]adminmod.RiskFlag, 0, len(flagged))
		for _, p := range flagged {
			out = append(out, adminmod.RiskFlag{
				AccountID: p.AccountID, RiskScore: p.RiskScore,
				RiskReasons: p.RiskReasons, ReviewStatus: string(p.ReviewStatus),
			})
		}
		return out, nil
	}
	adminPendingPayouts := func(ctx context.Context) (int, error) {
		return payoutRepo.CountPending(ctx)
	}
	toTransactionView := func(tx *ledger.Transaction) *adminmod.TransactionView {
		if tx == nil {
			return nil
		}
		entries := make([]adminmod.LedgerEntryView, 0, len(tx.Entries))
		for _, e := range tx.Entries {
			entries = append(entries, adminmod.LedgerEntryView{
				AccountID: e.AccountID, Amount: e.Amount, Currency: string(e.Currency),
			})
		}
		return &adminmod.TransactionView{
			ID: tx.ID, Kind: tx.Kind, Metadata: tx.Metadata, Entries: entries, CreatedAt: tx.CreatedAt,
		}
	}
	adminGetTransaction := func(ctx context.Context, transactionID string) (*adminmod.TransactionView, error) {
		tx, err := ledgerRepo.GetTransactionByID(ctx, transactionID)
		if err != nil {
			if errors.Is(err, ledger.ErrTransactionNotFound) {
				return nil, adminmod.ErrTransactionNotFound
			}
			return nil, err
		}
		return toTransactionView(tx), nil
	}
	adminReverseTransaction := func(ctx context.Context, transactionID, idempotencyKey, reason string) (*adminmod.TransactionView, error) {
		tx, _, err := ledgerRepo.ReverseTransaction(ctx, transactionID, idempotencyKey, reason)
		if err != nil {
			var insufficient *ledger.InsufficientBalanceError
			switch {
			case errors.Is(err, ledger.ErrTransactionNotFound):
				return nil, adminmod.ErrTransactionNotFound
			case errors.Is(err, ledger.ErrAlreadyReversed):
				return nil, adminmod.ErrAlreadyReversed
			case errors.Is(err, ledger.ErrCannotReverseAReversal):
				return nil, adminmod.ErrCannotReverse
			case errors.As(err, &insufficient):
				return nil, adminmod.ErrReversalWouldOverdraw
			default:
				return nil, err
			}
		}
		return toTransactionView(tx), nil
	}
	toPayoutView := func(p *creator.Payout) *adminmod.PayoutView {
		if p == nil {
			return nil
		}
		return &adminmod.PayoutView{
			ID: p.ID, AccountID: p.AccountID, AmountDiamonds: p.AmountDiamonds,
			Status: string(p.Status), PayoutReference: p.PayoutReference,
			AdminNote: p.AdminNote, FailureReason: p.FailureReason, ReviewedBy: p.ReviewedBy,
			RequestedAt: p.RequestedAt, ReviewedAt: p.ReviewedAt, PaidAt: p.PaidAt,
		}
	}
	adminListPayouts := func(ctx context.Context, statusFilter string) ([]adminmod.PayoutView, error) {
		list, err := creatorService.AdminListPayouts(ctx, creator.PayoutStatus(statusFilter))
		if err != nil {
			return nil, err
		}
		out := make([]adminmod.PayoutView, 0, len(list))
		for i := range list {
			out = append(out, *toPayoutView(&list[i]))
		}
		return out, nil
	}
	adminPayoutAction := func(ctx context.Context, action, payoutID, adminID, note string) (*adminmod.PayoutView, error) {
		var p *creator.Payout
		var err error
		switch action {
		case "approve":
			p, err = creatorService.AdminApprovePayout(ctx, adminID, payoutID, note)
		case "reject":
			p, err = creatorService.AdminRejectPayout(ctx, adminID, payoutID, note)
		case "processing":
			p, err = creatorService.AdminMarkProcessing(ctx, adminID, payoutID)
		case "paid":
			p, err = creatorService.AdminMarkPaid(ctx, adminID, payoutID, note)
		case "failed":
			p, err = creatorService.AdminMarkFailed(ctx, adminID, payoutID, note)
		default:
			return nil, fmt.Errorf("main: unknown payout action %q", action)
		}
		if err != nil {
			switch {
			case errors.Is(err, creator.ErrPayoutNotFound):
				return nil, adminmod.ErrPayoutNotFound
			case errors.Is(err, creator.ErrInvalidPayoutTransition):
				return nil, adminmod.ErrInvalidPayoutTransition
			default:
				return nil, err
			}
		}
		return toPayoutView(p), nil
	}
	var roleRepo adminsvc.RoleRepo
	var auditRepo adminsvc.AuditRepo
	var ticketRepo adminsvc.TicketRepo
	if dbPool != nil {
		roleRepo = adminpg.NewRoleRepo(dbPool)
		auditRepo = adminpg.NewAuditRepo(dbPool)
		ticketRepo = adminpg.NewTicketRepo(dbPool)
	} else {
		roleRepo = adminmod.NewMemRoleRepo()
		auditRepo = adminmod.NewMemAuditRepo()
		ticketRepo = adminmod.NewMemTicketRepo()
	}
	adminService := adminsvc.New(
		roleRepo, auditRepo, ticketRepo,
		adminGetAccount, adminSetAccountStatus, adminListGifts, adminLedgerIntegrity,
		adminActiveStreams, adminOpenReports, adminFlaggedRisk, adminPendingPayouts,
		adminListPayouts, adminPayoutAction,
		adminGetTransaction, adminReverseTransaction,
	)
	adminH := adminHandlers.New(adminService)

	// ── Analytics (Phase 17) ─────────────────────────────────────────────────
	analyticsRequirePermission := func(ctx context.Context, actorID string) (bool, error) {
		return adminService.HasPermission(ctx, actorID, adminmod.PermAnalyticsView)
	}
	analyticsListGifts := func(ctx context.Context, since time.Time) ([]analyticssvc.GiftTxn, error) {
		txs, err := ledgerRepo.ListByKind(ctx, "gift", since)
		if err != nil {
			return nil, err
		}
		out := make([]analyticssvc.GiftTxn, 0, len(txs))
		for _, tx := range txs {
			sender, _ := tx.Metadata["sender_id"].(string)
			recipient, _ := tx.Metadata["recipient_id"].(string)
			giftID, _ := tx.Metadata["gift_id"].(string)
			if sender == "" || recipient == "" {
				continue
			}
			var grossCoins, creatorDiamonds int64
			for _, e := range tx.Entries {
				switch e.AccountID {
				case ledger.UserCoinsAccount(sender):
					grossCoins = -e.Amount
				case ledger.UserDiamondsAccount(recipient):
					creatorDiamonds = e.Amount
				}
			}
			out = append(out, analyticssvc.GiftTxn{
				SenderID: sender, RecipientID: recipient, GiftID: giftID,
				GrossCoins: grossCoins, CreatorDiamonds: creatorDiamonds, CreatedAt: tx.CreatedAt,
			})
		}
		return out, nil
	}
	analyticsListPurchases := func(ctx context.Context, since time.Time) ([]analyticssvc.PurchaseTxn, error) {
		txs, err := ledgerRepo.ListByKind(ctx, "purchase", since)
		if err != nil {
			return nil, err
		}
		out := make([]analyticssvc.PurchaseTxn, 0, len(txs))
		for _, tx := range txs {
			acctID, _ := tx.Metadata["account_id"].(string)
			if acctID == "" {
				continue
			}
			var coins int64
			for _, e := range tx.Entries {
				if e.AccountID == ledger.UserCoinsAccount(acctID) {
					coins = e.Amount
				}
			}
			out = append(out, analyticssvc.PurchaseTxn{AccountID: acctID, Coins: coins, CreatedAt: tx.CreatedAt})
		}
		return out, nil
	}
	analyticsListBroadcasts := func(ctx context.Context) ([]analyticssvc.BroadcastSession, error) {
		sessions, err := streamRepo.ListAll(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]analyticssvc.BroadcastSession, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, analyticssvc.BroadcastSession{HostID: s.HostID, StartedAt: s.StartedAt})
		}
		return out, nil
	}
	var analyticsEventRepo analyticssvc.EventRepo
	if dbPool != nil {
		analyticsEventRepo = analyticspg.New(dbPool)
	} else {
		analyticsEventRepo = analytics.NewMemEventRepo()
	}
	analyticsService := analyticssvc.New(
		analyticsEventRepo, analyticsRequirePermission,
		analyticsListGifts, analyticsListPurchases, analyticsListBroadcasts,
	)
	analyticsH := analyticsHandlers.New(analyticsService)

	// Feed the event pipeline from real auth traffic (account_created,
	// login) without authsvc importing analytics (Phase 17's DI convention).
	authService.WithAuthEventHook(func(ctx context.Context, accountID, event string) {
		analyticsService.RecordEvent(ctx, accountID, analytics.EventType(event))
	})

	// ── Fraud prevention (Phase 18) ───────────────────────────────────────────
	fraudListRecentGifts := func(ctx context.Context, since time.Time) ([]fraudsvc.GiftFlow, error) {
		txs, err := ledgerRepo.ListByKind(ctx, "gift", since)
		if err != nil {
			return nil, err
		}
		out := make([]fraudsvc.GiftFlow, 0, len(txs))
		for _, tx := range txs {
			sender, _ := tx.Metadata["sender_id"].(string)
			recipient, _ := tx.Metadata["recipient_id"].(string)
			if sender == "" || recipient == "" {
				continue
			}
			out = append(out, fraudsvc.GiftFlow{SenderID: sender, RecipientID: recipient, CreatedAt: tx.CreatedAt})
		}
		return out, nil
	}
	fraudRequirePermission := func(ctx context.Context, actorID string) (bool, error) {
		return adminService.HasPermission(ctx, actorID, adminmod.PermFraudReview)
	}
	var fraudDeviceRepo fraudsvc.DeviceRepo
	var fraudDisputeRepo fraudsvc.DisputeRepo
	if dbPool != nil {
		fraudDeviceRepo = fraudpg.NewDeviceRepo(dbPool)
		fraudDisputeRepo = fraudpg.NewDisputeRepo(dbPool)
	} else {
		fraudDeviceRepo = fraudmod.NewMemDeviceRepo()
		fraudDisputeRepo = fraudmod.NewMemDisputeRepo()
	}
	fraudService := fraudsvc.New(
		fraudDeviceRepo, fraudDisputeRepo,
		riskEngine.AddSignal, fraudListRecentGifts, fraudRequirePermission,
	)
	fraudH := fraudHandlers.New(fraudService)

	// Feed device clustering, gift-loop, velocity, and chargeback signals
	// from real auth/gift/order traffic without those modules importing
	// fraud (Phase 18's DI convention — same pattern as moderation's hooks
	// on chatsvc/streaming).
	authService.WithDeviceLoginHook(fraudService.RecordDeviceLogin)
	giftService.
		WithVelocityCheck(func(ctx context.Context, senderID string) error {
			if err := fraudService.CheckGiftVelocity(ctx, senderID); err != nil {
				return giftsvc.ErrVelocityLimitExceeded
			}
			return nil
		}).
		WithSentHook(fraudService.CheckGiftLoop)
	orderService.WithDisputeHook(func(ctx context.Context, accountID, orderID string) {
		fraudService.RecordDispute(ctx, accountID)
	})

	// ── Feature flags, staged rollout, canary metrics (Phase 20) ─────────────
	rolloutRequirePermission := func(ctx context.Context, actorID string) (bool, error) {
		return adminService.HasPermission(ctx, actorID, adminmod.PermRolloutManage)
	}
	rolloutAudit := func(ctx context.Context, actorID, action, detail string) {
		logger.Info("rollout_audit", "actor_id", actorID, "action", action, "detail", detail)
	}
	// Canary metrics stay in-memory regardless of DATABASE_URL — see
	// migrations/0011_rollout.sql's doc comment: this process only ever
	// runs one binary, so its in-flight latency/error counters are
	// ephemeral process state, not durable data.
	var rolloutFlagRepo rolloutsvc.FlagRepo
	var rolloutInternalRepo rolloutsvc.InternalRepo
	if dbPool != nil {
		rolloutFlagRepo = rolloutpg.NewFlagRepo(dbPool)
		rolloutInternalRepo = rolloutpg.NewInternalRepo(dbPool)
	} else {
		rolloutFlagRepo = rollout.NewMemFlagRepo()
		rolloutInternalRepo = rollout.NewMemInternalRepo()
	}
	rolloutService := rolloutsvc.New(
		rolloutFlagRepo, rolloutInternalRepo, rollout.NewMemMetricsRepo(),
		rolloutRequirePermission, rolloutAudit,
	)
	rolloutH := rolloutHandlers.New(rolloutService, []string{rollout.CanaryFlagKey})

	// ── Router ────────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Auth (public)
	mux.HandleFunc("POST /api/v1/auth/phone/start", authH.PhoneStart)
	mux.HandleFunc("POST /api/v1/auth/phone/verify", authH.PhoneVerify)
	mux.HandleFunc("POST /api/v1/auth/email/register", authH.EmailRegister)
	mux.HandleFunc("POST /api/v1/auth/email/login", authH.EmailLogin)
	mux.HandleFunc("POST /api/v1/auth/refresh", authH.Refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", authH.Logout)

	// Auth (protected)
	mux.Handle("POST /api/v1/auth/age/declare", sharedAuth(issuer, http.HandlerFunc(authH.DeclareAge)))
	mux.Handle("POST /api/v1/auth/age/dev-assure", sharedAuth(issuer, http.HandlerFunc(authH.DevAssureAge)))
	mux.Handle("GET /api/v1/auth/age/status", sharedAuth(issuer, http.HandlerFunc(authH.AgeStatus)))
	mux.Handle("POST /api/v1/me/delete", sharedAuth(issuer, http.HandlerFunc(authH.RequestDeletion)))
	mux.Handle("POST /api/v1/me/delete/cancel", sharedAuth(issuer, http.HandlerFunc(authH.CancelDeletion)))

	// Profile (optional auth — guest can read)
	optAuth := func(h http.HandlerFunc) http.Handler {
		return sharedOptAuth(issuer, http.HandlerFunc(h))
	}
	mustAuth := func(h http.HandlerFunc) http.Handler {
		return sharedAuth(issuer, http.HandlerFunc(h))
	}

	// Wrap profile endpoints to auto-create profile on first access
	ensureProfile := func(h http.HandlerFunc) http.Handler {
		return mustAuth(func(w http.ResponseWriter, r *http.Request) {
			accountID := profileHandlers.AccountIDFromCtx(r.Context())
			if accountID != "" {
				// Auto-create profile if it doesn't exist (idempotent)
				_, _ = profileService.CreateProfile(r.Context(), accountID, accountID, "US")
			}
			h(w, r)
		})
	}
	mux.Handle("GET /api/v1/me", ensureProfile(profileH.GetMyProfile))
	mux.Handle("PATCH /api/v1/me", ensureProfile(profileH.UpdateProfile))
	mux.Handle("GET /api/v1/me/privacy", mustAuth(profileH.GetPrivacy))
	mux.Handle("PATCH /api/v1/me/privacy", mustAuth(profileH.UpdatePrivacy))
	mux.Handle("GET /api/v1/users/{id}", optAuth(func(w http.ResponseWriter, r *http.Request) {
		// Auto-create profile stub for any account that doesn't have one yet
		id := r.PathValue("id")
		if id != "" {
			_, _ = profileService.CreateProfile(r.Context(), id, id, "US")
		}
		profileH.GetProfile(w, r)
	}))
	mux.Handle("GET /api/v1/users/{id}/relationship", mustAuth(profileH.GetRelationship))
	mux.Handle("GET /api/v1/users/{id}/following", optAuth(profileH.GetFollowing))
	mux.Handle("GET /api/v1/users/{id}/followers", optAuth(profileH.GetFollowers))
	mux.Handle("POST /api/v1/follows", mustAuth(profileH.Follow))
	mux.Handle("DELETE /api/v1/follows/{followee_id}", mustAuth(profileH.Unfollow))
	mux.Handle("GET /api/v1/me/blocked", mustAuth(profileH.GetBlocked))
	mux.Handle("POST /api/v1/blocks", mustAuth(profileH.Block))
	mux.Handle("DELETE /api/v1/blocks/{id}", mustAuth(profileH.Unblock))

	// Feed (guest-accessible)
	mux.Handle("GET /api/v1/feed", optAuth(feedH.GetFeed))
	mux.Handle("GET /api/v1/rooms/{id}", optAuth(feedH.GetRoom))
	mux.Handle("POST /api/v1/rooms/{id}/premium", mustAuth(feedH.SetRoomPremium))
	mux.Handle("POST /api/v1/rooms/{id}/unlock", mustAuth(feedH.UnlockRoom))
	mux.Handle("POST /api/v1/streams", mustAuth(feedH.CreateRoom))
	mux.Handle("POST /api/v1/streams/{id}/stop", mustAuth(publishStreamEnded(realtimeBus, feedH.EndRoom)))
	// Streaming session routes (Phase 5)
	mux.Handle("POST /api/v1/streams/{id}/session", mustAuth(streamH.StartBroadcast))
	mux.Handle("POST /api/v1/streams/{id}/session/stop", mustAuth(streamH.EndBroadcast))
	mux.Handle("GET /api/v1/rooms/{id}/stream", optAuth(streamH.GetStream))
	mux.Handle("GET /api/v1/streams/{id}/health", mustAuth(streamH.GetHealth))
	mux.Handle("GET /api/v1/search", optAuth(feedH.Search))
	mux.Handle("GET /api/v1/notifications", mustAuth(feedH.GetNotifications))
	mux.Handle("GET /api/v1/notifications/summary", optAuth(feedH.GetNotificationSummary))
	mux.Handle("POST /api/v1/notifications/read", mustAuth(feedH.MarkNotificationsRead))

	// Realtime gateway (Phase 6): short-lived single-use ws token exchange
	// (doc 08 §1), then the WebSocket upgrade itself.
	mux.Handle("GET /api/v1/auth/ws-token", mustAuth(func(w http.ResponseWriter, r *http.Request) {
		accountID := profileHandlers.AccountIDFromCtx(r.Context())
		device := r.URL.Query().Get("device")
		tok := wsTokens.Issue(accountID, device)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ws_token": tok, "expires_in_s": 60})
	}))
	mux.Handle("GET /ws", wsHandler)

	// Private chat (Phase 7)
	mux.Handle("GET /api/v1/conversations", mustAuth(chatH.ListConversations))
	mux.Handle("POST /api/v1/conversations", mustAuth(chatH.StartConversation))
	mux.Handle("GET /api/v1/conversations/{id}/messages", mustAuth(chatH.ListMessages))
	mux.Handle("POST /api/v1/conversations/{id}/messages", mustAuth(chatH.SendMessage))
	mux.Handle("POST /api/v1/conversations/{id}/read", mustAuth(chatH.MarkRead))
	mux.Handle("POST /api/v1/conversations/{id}/accept", mustAuth(chatH.AcceptRequest))
	mux.Handle("POST /api/v1/conversations/{id}/decline", mustAuth(chatH.DeclineRequest))
	mux.Handle("DELETE /api/v1/messages/{id}", mustAuth(chatH.DeleteMessage))

	// Attachments (Phase 10)
	mux.Handle("POST /api/v1/attachments", mustAuth(attachH.CreateUploadSlot))
	mux.Handle("PUT /api/v1/attachments/{id}/upload", mustAuth(attachH.Upload))
	mux.Handle("GET /api/v1/attachments/{id}", mustAuth(attachH.Get))
	mux.Handle("GET /api/v1/attachments/{id}/download", mustAuth(attachH.Download))

	// Gifts / wallet (Phase 8)
	mux.Handle("GET /api/v1/gifts/catalogue", optAuth(giftH.Catalogue))
	mux.Handle("POST /api/v1/gifts/send", mustAuth(recordGiftAttempt(rolloutService, giftH.SendGift)))
	mux.Handle("GET /api/v1/wallet/balance", mustAuth(giftH.GetBalance))
	mux.Handle("GET /api/v1/wallet/transactions", mustAuth(giftH.TransactionHistory))
	mux.Handle("POST /api/v1/wallet/dev-topup", mustAuth(giftH.DevTopUp))

	// Orders / spend limits (Phase 9)
	mux.Handle("GET /api/v1/products", optAuth(giftH.Products))
	mux.Handle("POST /api/v1/orders", mustAuth(giftH.CreateOrder))
	mux.Handle("POST /api/v1/orders/{id}/verify", mustAuth(giftH.VerifyOrder))
	mux.Handle("GET /api/v1/orders", mustAuth(giftH.ListOrders))
	mux.Handle("GET /api/v1/orders/{id}", mustAuth(giftH.GetOrder))
	mux.Handle("POST /api/v1/orders/{id}/dispute", mustAuth(giftH.DisputeOrder))
	mux.Handle("GET /api/v1/me/spend-limits", mustAuth(giftH.GetSpendLimits))
	mux.Handle("PATCH /api/v1/me/spend-limits", mustAuth(giftH.UpdateSpendLimits))

	// Referral / invite rewards
	mux.Handle("POST /api/v1/me/referrals/claim", mustAuth(referralH.ClaimCode))
	mux.Handle("GET /api/v1/me/referrals", mustAuth(referralH.ListMyReferrals))

	// Private video (Phase 11)
	mux.Handle("POST /api/v1/private-video/requests", mustAuth(pvH.RequestSession))
	mux.Handle("POST /api/v1/private-video/sessions/{id}/accept", mustAuth(pvH.AcceptSession))
	mux.Handle("POST /api/v1/private-video/sessions/{id}/decline", mustAuth(pvH.DeclineSession))
	mux.Handle("POST /api/v1/private-video/sessions/{id}/end", mustAuth(pvH.EndSession))
	mux.Handle("GET /api/v1/private-video/sessions/{id}", mustAuth(pvH.GetSession))
	mux.Handle("GET /api/v1/private-video/sessions", mustAuth(pvH.ListSessions))

	// Matchmaking (Phase 12)
	mux.Handle("POST /api/v1/match/requests", mustAuth(matchH.CreateRequest))
	mux.Handle("GET /api/v1/match/requests/{id}", mustAuth(matchH.GetRequest))
	mux.Handle("DELETE /api/v1/match/requests/{id}", mustAuth(matchH.CancelRequest))
	mux.Handle("POST /api/v1/match/decisions", mustAuth(matchH.Decide))
	mux.Handle("GET /api/v1/match/history", mustAuth(matchH.ListHistory))
	mux.Handle("GET /api/v1/me/match-preferences", mustAuth(matchH.GetPreferences))
	mux.Handle("PATCH /api/v1/me/match-preferences", mustAuth(matchH.UpdatePreferences))

	// Creator dashboard (Phase 14)
	mux.Handle("GET /api/v1/me/creator/kyc", mustAuth(creatorH.GetKYC))
	mux.Handle("POST /api/v1/me/creator/kyc", mustAuth(creatorH.SubmitKYC))
	mux.Handle("POST /api/v1/me/creator/kyc/dev-approve", mustAuth(creatorH.DevApproveKYC))
	mux.Handle("GET /api/v1/me/creator/earnings", mustAuth(creatorH.GetEarnings))
	mux.Handle("GET /api/v1/me/creator/analytics", mustAuth(creatorH.GetAnalytics))
	mux.Handle("POST /api/v1/me/creator/payouts", mustAuth(creatorH.RequestPayout))
	mux.Handle("GET /api/v1/me/creator/payouts", mustAuth(creatorH.ListPayouts))
	mux.Handle("GET /api/v1/me/creator/payout-profile", mustAuth(creatorH.GetPayoutProfile))
	mux.Handle("PUT /api/v1/me/creator/payout-profile", mustAuth(creatorH.SetPayoutProfile))
	mux.Handle("GET /api/v1/payout-countries", optAuth(creatorH.ListPayoutCountries))

	// Moderation (Phase 15)
	mux.Handle("POST /api/v1/reports", mustAuth(modH.SubmitReport))
	mux.Handle("GET /api/v1/policies/rules", optAuth(modH.ListRules))
	mux.Handle("GET /api/v1/me/enforcements", mustAuth(modH.ListMyEnforcements))
	mux.Handle("GET /api/v1/me/enforcements/{id}", mustAuth(modH.GetEnforcement))
	mux.Handle("POST /api/v1/me/enforcements/{id}/appeal", mustAuth(modH.FileAppeal))
	mux.Handle("GET /api/v1/moderation/queue", mustAuth(modH.ListQueue))
	mux.Handle("POST /api/v1/moderation/reports/{id}/triage", mustAuth(modH.TriageReport))
	mux.Handle("POST /api/v1/moderation/enforcements", mustAuth(modH.CreateEnforcement))
	mux.Handle("GET /api/v1/moderation/appeals", mustAuth(modH.ListAppealQueue))
	mux.Handle("POST /api/v1/moderation/appeals/{id}/decide", mustAuth(modH.DecideAppeal))
	mux.Handle("GET /api/v1/moderation/risk-queue", mustAuth(modH.ListRiskQueue))
	mux.Handle("POST /api/v1/moderation/dev-grant", mustAuth(modH.DevGrantModerator))

	// Support tickets (Phase 16, doc 07 §11 / BT-08)
	mux.Handle("POST /api/v1/support/tickets", mustAuth(adminH.CreateTicket))
	mux.Handle("GET /api/v1/support/tickets", mustAuth(adminH.ListMyTickets))
	mux.Handle("GET /api/v1/support/tickets/{id}", mustAuth(adminH.GetTicket))
	mux.Handle("POST /api/v1/support/tickets/{id}/messages", mustAuth(adminH.ReplyTicket))

	// Admin console (Phase 16, RBAC-gated)
	mux.Handle("GET /api/v1/admin/me/role", mustAuth(adminH.GetMyRole))
	mux.Handle("POST /api/v1/admin/dev-grant", mustAuth(adminH.DevGrantRole))
	mux.Handle("GET /api/v1/admin/accounts/{id}", mustAuth(adminH.GetAccount))
	mux.Handle("POST /api/v1/admin/accounts/{id}/restrict", mustAuth(adminH.RestrictAccount))
	mux.Handle("POST /api/v1/admin/accounts/{id}/unsuspend", mustAuth(adminH.UnsuspendAccount))
	mux.Handle("GET /api/v1/admin/economy/anomalies", mustAuth(adminH.ListEconomyAnomalies))
	mux.Handle("GET /api/v1/admin/fraud/queue", mustAuth(adminH.ListFraudQueue))
	mux.Handle("GET /api/v1/admin/support/queue", mustAuth(adminH.ListTicketQueue))
	mux.Handle("POST /api/v1/admin/support/tickets/{id}/resolve", mustAuth(adminH.ResolveTicket))
	mux.Handle("GET /api/v1/admin/health", mustAuth(adminH.GetPlatformHealth))
	mux.Handle("GET /api/v1/admin/audit-log", mustAuth(adminH.ListAuditLog))
	mux.Handle("GET /api/v1/admin/payouts", mustAuth(adminH.ListPayouts))
	mux.Handle("POST /api/v1/admin/payouts/{id}/approve", mustAuth(adminH.ApprovePayout))
	mux.Handle("POST /api/v1/admin/payouts/{id}/reject", mustAuth(adminH.RejectPayout))
	mux.Handle("POST /api/v1/admin/payouts/{id}/processing", mustAuth(adminH.MarkPayoutProcessing))
	mux.Handle("POST /api/v1/admin/payouts/{id}/paid", mustAuth(adminH.MarkPayoutPaid))
	mux.Handle("POST /api/v1/admin/payouts/{id}/failed", mustAuth(adminH.MarkPayoutFailed))
	mux.Handle("GET /api/v1/admin/transactions/{id}", mustAuth(adminH.GetTransaction))
	mux.Handle("POST /api/v1/admin/transactions/{id}/reverse", mustAuth(adminH.ReverseTransaction))

	// Analytics (Phase 17)
	mux.Handle("GET /api/v1/analytics/dau-mau", mustAuth(analyticsH.GetDAUMAU))
	mux.Handle("GET /api/v1/analytics/retention", mustAuth(analyticsH.GetRetention))
	mux.Handle("GET /api/v1/analytics/funnel", mustAuth(analyticsH.GetFunnel))
	mux.Handle("GET /api/v1/analytics/economy", mustAuth(analyticsH.GetEconomySummary))
	mux.Handle("GET /api/v1/creator/analytics", mustAuth(analyticsH.GetCreatorAnalytics))

	// Fraud prevention (Phase 18)
	mux.Handle("GET /api/v1/fraud/device-clusters", mustAuth(fraudH.GetDeviceCluster))

	// Feature flags / staged rollout / canary metrics (Phase 20)
	mux.Handle("GET /api/v1/config", optAuth(rolloutH.GetConfig))
	mux.Handle("GET /api/v1/rollout/flags", mustAuth(rolloutH.ListFlags))
	mux.Handle("POST /api/v1/rollout/flags", mustAuth(rolloutH.SetStage))
	mux.Handle("GET /api/v1/rollout/canary-metrics", mustAuth(rolloutH.GetCanaryMetrics))
	mux.Handle("POST /api/v1/rollout/dev-mark-internal", mustAuth(rolloutH.DevMarkInternal))

	// Health + version
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"phases":  []string{"2-auth", "3-profile", "4-feed", "5-streaming", "6-realtime", "7-chat", "8-gifts", "9-wallet", "10-attachments", "11-private-video", "12-matchmaking", "13-translation", "14-creator-dashboard", "15-moderation", "16-admin-dashboard", "17-analytics", "18-fraud-prevention", "19-performance-testing", "20-production-deployment"},
			"version": "1.0.0",
			"engine":  engine.Name(),
		})
	})

	// DEBUG endpoint — remove before production
	mux.Handle("GET /api/v1/debug/ctx", sharedAuth(issuer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := profileHandlers.AccountIDFromCtx(r.Context())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"account_id_in_ctx": id, "has_account": id != ""})
	})))

	// Legal pages — required by Google Play Console's Data Safety / store
	// listing, and linked from the mobile app's Settings screen (see
	// legal.go's doc comment for the "draft, not legal advice" caveat).
	mux.HandleFunc("GET /privacy-policy", servePrivacyPolicy)
	mux.HandleFunc("GET /terms-of-service", serveTermsOfService)

	// Web UI — serve the React app from the embedded HTML
	mux.HandleFunc("/", serveWebUI)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain(mux, requestLogger(logger), canaryMetricsMiddleware(rolloutService, issuer), corsMiddleware),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	if statePath != "" && dbPool == nil {
		saveCtx, cancelSave := context.WithCancel(context.Background())
		defer cancelSave()
		startPeriodicSave(saveCtx, logger, statePath, localRepos, 30*time.Second)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("lumena api server starting",
			"port", port,
			"phases", "2-auth,3-profile,4-feed,5-streaming,6-realtime,7-chat,8-gifts,9-wallet,10-attachments,11-private-video,12-matchmaking,13-translation",
			"web_ui", "http://localhost:"+port,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-stop:
		logger.Info("shutting down")
		if statePath != "" && dbPool == nil {
			saveLocalState(logger, statePath, localRepos)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}

// sharedAuth is the canonical auth middleware for the unified server.
// It verifies the JWT and writes the account_id to the context using
// profileHandlers.WithAccountID — the single source of truth for the
// account_id context key across all downstream handlers (profile, feed).
// This eliminates the cross-package context key mismatch.
func sharedAuth(issuer *token.Issuer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
				"code": "UNAUTHENTICATED", "message": "Bearer token required.",
			}})
			return
		}
		rawToken := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := issuer.VerifyAccessToken(rawToken)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
				"code": "UNAUTHENTICATED", "message": "Invalid or expired token.",
			}})
			return
		}
		// Write with profileHandlers.WithAccountID — the single canonical key
		ctx := profileHandlers.WithAccountID(r.Context(), claims.AccountID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sharedOptAuth(issuer *token.Issuer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			rawToken := strings.TrimPrefix(authHeader, "Bearer ")
			if claims, err := issuer.VerifyAccessToken(rawToken); err == nil {
				ctx := profileHandlers.WithAccountID(r.Context(), claims.AccountID)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// serveWebUI serves the built-in React web UI.
// In production this would serve from a CDN or a build artifact.
// For development: serves the embedded HTML that connects to /api/v1/*.
func serveWebUI(w http.ResponseWriter, r *http.Request) {
	// Only serve the UI for non-API routes
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, lumenaWebUI)
}

// ── Middleware ─────────────────────────────────────────────────────────────────

func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

// canaryMetricsMiddleware records the per-request latency/status/panic
// data behind Phase 20's canary metrics (error rate, p99 latency, crash
// rate — gift success rate is recorded separately, at the gift-send
// route, since it isn't visible from status code alone). It also doubles
// as this server's only panic-recovery layer: a panicking handler
// previously took the whole process down mid-request; now it's caught,
// logged, counted as a crash for the calling account's cohort, and
// answered with a normal 500 instead of dropping the connection.
func canaryMetricsMiddleware(rolloutSvc *rolloutsvc.Service, issuer *token.Issuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			accountID := ""
			if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
				if claims, err := issuer.VerifyAccessToken(strings.TrimPrefix(authHeader, "Bearer ")); err == nil {
					accountID = claims.AccountID
				}
			}
			cohort := rolloutSvc.CohortFor(r.Context(), accountID)

			rw := &statusWriter{ResponseWriter: w, status: 200}
			start := time.Now()
			defer func() {
				if rec := recover(); rec != nil {
					rolloutSvc.RecordPanic(r.Context(), cohort)
					slog.Default().Error("panic recovered", "err", rec, "path", r.URL.Path)
					if rw.status == 200 { // headers not yet written
						rw.Header().Set("Content-Type", "application/json")
						rw.WriteHeader(http.StatusInternalServerError)
						_ = json.NewEncoder(rw).Encode(map[string]any{"error": map[string]string{
							"code": "INTERNAL_ERROR", "message": "Something went wrong.",
						}})
					}
				}
				rolloutSvc.RecordRequest(r.Context(), cohort, rw.status, time.Since(start))
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				return // don't log static asset requests
			}
			logger.Info("req",
				"method", r.Method, "path", r.URL.Path,
				"status", rw.status, "ms", time.Since(start).Milliseconds())
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}
func (w *statusWriter) WriteHeader(s int) { w.status = s; w.ResponseWriter.WriteHeader(s) }

// Hijack forwards to the underlying ResponseWriter so this wrapper doesn't
// break the WebSocket upgrade, which requires http.Hijacker.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hj.Hijack()
}

// publishStreamEnded wraps the EndRoom handler so a successful stop also
// broadcasts STREAM_ENDED on the room's realtime topic (doc 08 §4). Viewers
// still connected via WebSocket see the room end immediately instead of
// finding out only on their next REST poll.
func publishStreamEnded(b *bus.Bus, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.PathValue("id")
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next(rw, r)
		if rw.status >= 200 && rw.status < 300 {
			b.Publish("room:"+roomID, "STREAM_ENDED", map[string]any{
				"room_id": roomID,
				"reason":  "host_ended",
			})
		}
	}
}

// recordGiftAttempt wraps the gift-send handler to feed Phase 20's "gift
// success rate" canary metric — the one metric not derivable from HTTP
// status alone (a 402 insufficient-balance is a legitimate 4xx, not a
// server error, but it IS a failed gift attempt from the product's point
// of view).
func recordGiftAttempt(rolloutSvc *rolloutsvc.Service, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := profileHandlers.AccountIDFromCtx(r.Context())
		cohort := rolloutSvc.CohortFor(r.Context(), accountID)
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next(rw, r)
		rolloutSvc.RecordGiftAttempt(r.Context(), cohort, rw.status >= 200 && rw.status < 300)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,Idempotency-Key,X-Region-Code")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// lumenaWebUI is the embedded single-page web application.
// Defined in webui.go in the same package.
var lumenaWebUI string
