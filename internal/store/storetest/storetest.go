package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Factory returns a freshly-opened Store backed by per-test temporary
// resources. The returned cleanup is called from t.Cleanup; test code
// does not need to invoke it manually.
type Factory func(t *testing.T) (s store.Store, cleanup func())

// Run executes every compliance test against the backend produced by f.
// Invoke from each backend's _test.go with t.Run("compliance", ...).
func Run(t *testing.T, f Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"PrincipalsCRUD", testPrincipalsCRUD},
		{"PrincipalConflict", testPrincipalConflict},
		{"PrincipalConflict_ErrorStringIsClean", testPrincipalConflictErrorStringIsClean},
		{"DomainsCRUD", testDomainsCRUD},
		{"DomainConflict_ErrorStringIsClean", testDomainConflictErrorStringIsClean},
		{"AliasesCRUDAndResolve", testAliases},
		{"OIDCProviderAndLinks", testOIDC},
		{"APIKeys", testAPIKeys},
		{"MailboxesCRUD", testMailboxesCRUD},
		{"MailboxConflict", testMailboxConflict},
		{"MailboxConflict_ErrorStringIsClean", testMailboxConflictErrorStringIsClean},
		{"InsertMessageAllocatesUIDAndModSeq", testInsertMessageAllocatesUIDAndModSeq},
		{"UpdateFlagsBumpsModSeq", testUpdateFlagsBumpsModSeq},
		{"UpdateFlagsUnchangedSince", testUpdateFlagsUnchangedSince},
		{"UpdateFlagsKeywords", testUpdateFlagsKeywords},
		{"ExpungeMessages", testExpungeMessages},
		{"ChangeFeedMonotonic", testChangeFeedMonotonic},
		{"QuotaEnforcement", testQuotaEnforcement},
		{"DeleteMailboxCascades", testDeleteMailboxCascades},
		{"BlobRoundTrip", testBlobRoundTrip},
		{"BlobDedup", testBlobDedup},
		{"BlobNotFound", testBlobNotFound},
		{"FTSSmoke", testFTSSmoke},
		{"FTSCursor_GetSet_EmptyIsZero", testFTSCursorEmptyIsZero},
		{"FTSCursor_Upsert_Roundtrip", testFTSCursorUpsertRoundtrip},
		{"FTSCursor_Concurrent", testFTSCursorConcurrent},
		{"DeletePrincipal_Cascade", testDeletePrincipalCascade},
		{"ListPrincipals_Keyset", testListPrincipalsKeyset},
		{"DeleteOIDCProvider_Cascade", testDeleteOIDCProviderCascade},
		{"ListOIDCProviders", testListOIDCProviders},
		{"UnlinkOIDC_NotFoundWhenAbsent", testUnlinkOIDCNotFound},
		{"AuditLog_AppendAndList", testAuditLogAppendAndList},
		{"PrincipalFlagTOTPEnabled", testPrincipalFlagTOTPEnabled},
		{"GetMailboxByName", testGetMailboxByName},
		{"ListMessagesPagination", testListMessagesPagination},
		{"ListMessages_ReceivedBefore", testListMessagesReceivedBefore},
		{"SetMailboxSubscribed", testSetMailboxSubscribed},
		{"RenameMailbox", testRenameMailbox},
		{"SieveScript_EmptyReturnsEmpty", testSieveScriptEmpty},
		{"SieveScript_SetGetRoundtrip", testSieveScriptRoundtrip},
		{"SieveScript_Overwrite", testSieveScriptOverwrite},
		{"SieveScript_CascadeOnDeletePrincipal", testSieveScriptCascade},
		{"ListAliases_ByDomain", testListAliasesByDomain},
		{"DeleteAlias_NotFoundWhenAbsent", testDeleteAliasNotFound},
		{"DeleteDomain_NotFoundWhenAbsent", testDeleteDomainNotFound},
		{"ListAPIKeysByPrincipal", testListAPIKeysByPrincipal},
		{"DeleteAPIKey_NotFoundWhenAbsent", testDeleteAPIKeyNotFound},
		{"ListOIDCLinksByPrincipal", testListOIDCLinksByPrincipal},
		// -- Phase 2 Wave 2.0 --------------------------------------
		{"QueueEnqueueAndList", testQueueEnqueueAndList},
		{"QueueIdempotency", testQueueIdempotency},
		{"QueueClaimDueTransitionsState", testQueueClaimDueTransitionsState},
		{"QueueCompleteSuccessVsFailure", testQueueCompleteSuccessVsFailure},
		{"QueueRescheduleBumpsAttempts", testQueueRescheduleBumpsAttempts},
		{"QueueHoldRelease", testQueueHoldRelease},
		{"QueueDeleteCascadeOnPrincipalDelete", testQueueDeleteCascadeOnPrincipalDelete},
		{"QueueCountByState", testQueueCountByState},
		{"DKIMUpsertAndList", testDKIMUpsertAndList},
		{"DKIMRotateOneTx", testDKIMRotateOneTx},
		{"DKIMActiveLookup", testDKIMActiveLookup},
		{"ACMEAccountOrderCertLifecycle", testACMEAccountOrderCertLifecycle},
		{"ACMECertExpiringList", testACMECertExpiringList},
		{"WebhookCRUD", testWebhookCRUD},
		{"WebhookActiveForDomain", testWebhookActiveForDomain},
		{"WebhookSyntheticAndExtractedRoundTrip", testWebhookSyntheticAndExtractedRoundTrip},
		{"DMARCInsertAndAggregate", testDMARCInsertAndAggregate},
		{"DMARCDeduplicates", testDMARCDeduplicates},
		{"MailboxACLGrantListRevoke", testMailboxACLGrantListRevoke},
		{"MailboxACLAnyoneRow", testMailboxACLAnyoneRow},
		{"JMAPStatesIncrementAtomic", testJMAPStatesIncrementAtomic},
		{"TLSRPTAppendAndRange", testTLSRPTAppendAndRange},
		// -- Wave 2.2.5 JMAP persistence ---------------------------
		{"EmailSubmission_InsertGet_Roundtrip", testEmailSubmissionInsertGetRoundtrip},
		{"EmailSubmission_List_FilterByIdentity_FilterByUndoStatus_FilterByTimeRange", testEmailSubmissionListFilters},
		{"EmailSubmission_UpdateUndoStatus_Reflected_OnGet", testEmailSubmissionUpdateUndoStatus},
		{"EmailSubmission_Delete_NotFoundAfter", testEmailSubmissionDeleteNotFoundAfter},
		{"EmailSubmission_External_Flag_Roundtrip", testEmailSubmissionExternalFlagRoundtrip},
		{"JMAPIdentity_InsertGet_Roundtrip", testJMAPIdentityInsertGetRoundtrip},
		{"JMAPIdentity_List_ByPrincipal", testJMAPIdentityListByPrincipal},
		{"JMAPIdentity_Update_RoundTrips", testJMAPIdentityUpdateRoundtrips},
		{"JMAPIdentity_Delete_NotFoundAfter", testJMAPIdentityDeleteNotFoundAfter},
		{"JMAPIdentity_Signature_RoundTrip", testJMAPIdentitySignatureRoundTrip},
		// -- Wave 2.5 (REQ-PROTO-53/56/57; REQ-STORE-34/35) --------
		{"Mailbox_Color_RoundTrip", testMailboxColorRoundTrip},
		{"Mailbox_Color_RejectsInvalidFormat", testMailboxColorRejectsInvalid},
		{"JMAPStates_SieveCounter", testJMAPStatesSieveCounter},
		// -- REQ-PROTO-49 JMAP snooze ------------------------------
		{"Snooze_SetGet_Roundtrip", testSnoozeSetGetRoundtrip},
		{"Snooze_Clear", testSnoozeClear},
		{"Snooze_StateChangeAppended", testSnoozeStateChangeAppended},
		{"Snooze_ListDue_OnlyReturnsDue", testSnoozeListDueOnlyReturnsDue},
		{"Snooze_ListDue_Limit", testSnoozeListDueLimit},
		{"Snooze_ListDue_RequiresKeyword", testSnoozeListDueRequiresKeyword},
		// -- REQ-FILT-200..221 LLM categorisation -----------------
		{"CategorisationConfig_DefaultsSeededOnFirstRead", testCategorisationConfigDefaults},
		{"CategorisationConfig_RoundTrip", testCategorisationConfigRoundtrip},
		{"CategorisationConfig_GuardrailRoundTrip", testCategorisationConfigGuardrailRoundtrip},
		{"CategorisationConfig_DerivedCategoriesRoundTrip", testCategorisationConfigDerivedCategoriesRoundtrip},
		{"CategorisationConfig_PromptChangeClearsDerived", testCategorisationConfigPromptChangeClearsDerived},
		{"CategorisationConfig_EpochGuard_StaleWriteDropped", testCategorisationConfigEpochGuardStaleWriteDropped},
		{"CategorisationConfig_EpochGuard_PromptChangeBumpsEpoch", testCategorisationConfigEpochBumpsOnPromptChange},
		// -- REQ-FILT-66 / REQ-FILT-216 / G14 LLM classification records --
		{"LLMClassification_SetGet_SpamOnly", testLLMClassificationSpamOnly},
		{"LLMClassification_SetGet_CategoryOnly", testLLMClassificationCategoryOnly},
		{"LLMClassification_SetGet_BothFields", testLLMClassificationBothFields},
		{"LLMClassification_Upsert_SpamThenCategory", testLLMClassificationUpsertSpamThenCategory},
		{"LLMClassification_BatchGet", testLLMClassificationBatchGet},
		{"LLMClassification_GetNotFound", testLLMClassificationGetNotFound},
		// -- Wave 2.7 JMAP for Calendars (REQ-PROTO-54) -----------
		{"Calendar_InsertGet_Roundtrip", testCalendarInsertGetRoundtrip},
		{"Calendar_List_FilterAndPagination", testCalendarListFilterAndPagination},
		{"Calendar_Update_BumpsModSeqAndAppendsStateChange", testCalendarUpdateBumpsModSeq},
		{"Calendar_DefaultEnforcement_AutoFlipsPrior", testCalendarDefaultEnforcement},
		{"Calendar_Delete_CascadesEvents", testCalendarDeleteCascadesEvents},
		{"CalendarEvent_InsertGet_Roundtrip_IncludingJSON", testCalendarEventInsertGetRoundtrip},
		{"CalendarEvent_List_FilterByStartWindow", testCalendarEventListFilterByStartWindow},
		{"CalendarEvent_List_FilterByUID", testCalendarEventListFilterByUID},
		{"CalendarEvent_List_FilterByTextOnSummary", testCalendarEventListFilterByText},
		{"CalendarEvent_Update_BumpsModSeq", testCalendarEventUpdateBumpsModSeq},
		{"CalendarEvent_Delete_AppendsStateChange", testCalendarEventDeleteAppendsStateChange},
		{"CalendarEvent_GetByUID_NotFound_HappyPath", testCalendarEventGetByUID},
		{"JMAPStates_CalendarCounters_RaceTested", testJMAPStatesCalendarCounters},
		{"DeletePrincipal_CascadesCalendars", testDeletePrincipalCascadesCalendars},
		// -- Wave 2.8 chat (REQ-CHAT-*) ---------------------------
		{"ChatConversation_InsertGet_Roundtrip", testChatConversationInsertGetRoundtrip},
		{"ChatConversation_List_FilterByKind", testChatConversationListFilterByKind},
		{"ChatConversation_Update_BumpsModSeq", testChatConversationUpdateBumpsModSeq},
		{"ChatConversation_Delete_CascadesChildren", testChatConversationDeleteCascades},
		{"ChatMembership_InsertUniquePerConversationPrincipal", testChatMembershipUnique},
		{"ChatMembership_ListByConversation_ListByPrincipal", testChatMembershipList},
		{"ChatMembership_MuteRoundTrip", testChatMembershipMute},
		{"ChatMembership_LastReadRoundTrip", testChatMembershipLastRead},
		{"ChatMessage_InsertGet_Roundtrip", testChatMessageInsertGet},
		{"ChatMessage_List_TimeWindow", testChatMessageListTimeWindow},
		{"ChatMessage_Update_Edit", testChatMessageUpdateEdit},
		{"ChatMessage_SoftDelete_PreservesRow", testChatMessageSoftDelete},
		{"ChatMessage_AttachmentsShape", testChatMessageAttachmentsShape},
		{"ChatMessage_ReactionsShape", testChatMessageReactionsShape},
		{"ChatReaction_AddAndRemoveAtomic", testChatReactionAddRemove},
		{"ChatBlock_InsertListIsBlocked", testChatBlockInsertListIsBlocked},
		{"ChatBlock_RejectsSelfBlock", testChatBlockRejectsSelf},
		{"DeletePrincipal_CascadesChat", testDeletePrincipalCascadesChat},
		// -- Wave 2.9.6 chat features (REQ-CHAT-20/32/92) ----------
		{"ChatAccountSettings_DefaultsWhenAbsent", testChatAccountSettingsDefaults},
		{"ChatAccountSettings_UpsertRoundTrip", testChatAccountSettingsUpsertRoundTrip},
		{"ChatConversation_RetentionAndEditWindowRoundTrip", testChatConversationRetentionEditWindowRoundTrip},
		{"ChatRetention_HardDeleteAndRecount", testChatRetentionHardDeleteAndRecount},
		{"ChatRetention_ListConversationsForRetention", testChatRetentionListConversationsForRetention},
		{"ChatAttachment_BlobRefcountOnHardDelete", testChatAttachmentBlobRefcountOnHardDelete},
		{"ChatAttachment_DecRefUnderflowGuard", testChatAttachmentDecRefUnderflowGuard},
		// -- re #47: per-member change-feed fanout --------------------
		{"ChatChangeFeed_InsertMessage_FansToAllMembers", testChatChangeFeedInsertMessageFansToAllMembers},
		{"ChatChangeFeed_InsertMembership_FansToExistingMembers", testChatChangeFeedInsertMembershipFansToExistingMembers},
		// -- re #47: DM server-side deduplication ---------------------
		{"FindDMBetween_NotFound_WhenNoDM", testFindDMBetweenNotFound},
		{"FindDMBetween_Found_AfterInsertDM", testFindDMBetweenFound},
		{"FindDMBetween_Symmetric_BothOrders", testFindDMBetweenSymmetric},
		{"InsertDMConversation_ReturnsConflict_OnDuplicate", testInsertDMConversationConflictOnDuplicate},
		{"InsertDMConversation_SelfDM_Rejected", testInsertDMConversationSelfRejected},
		{"InsertDMConversation_DistinctPairs", testInsertDMConversationDistinctPairs},
		// -- Wave 3.8a JMAP PushSubscription (REQ-PROTO-120..122) --
		{"PushSubscription_InsertGet_Roundtrip", testPushSubscriptionInsertGetRoundtrip},
		{"PushSubscription_ListByPrincipal", testPushSubscriptionListByPrincipal},
		{"PushSubscription_Update_AppliesMutableFields", testPushSubscriptionUpdate},
		{"PushSubscription_Delete_NotFoundAfter", testPushSubscriptionDeleteNotFoundAfter},
		{"PushSubscription_CascadeOnPrincipalDelete", testPushSubscriptionCascadeOnPrincipalDelete},
		// -- Phase 3 Wave 3.9 Email reactions (REQ-PROTO-100..103) ---------
		{"EmailReaction_AddRemoveIdempotent", testEmailReactionAddRemoveIdempotent},
		{"EmailReaction_ListEmpty", testEmailReactionListEmpty},
		{"EmailReaction_BatchList", testEmailReactionBatchList},
		{"EmailReaction_GetMessageByMessageIDHeader", testGetMessageByMessageIDHeader},
		// -- Principal/query support (REQ-CHAT-01b/c) --------------------
		{"SearchPrincipalsByText_DisplayNameMatch", testSearchPrincipalsByTextDisplayNameMatch},
		{"SearchPrincipalsByText_EmailLocalPartMatch", testSearchPrincipalsByTextEmailLocalPartMatch},
		{"SearchPrincipalsByText_SortOrder", testSearchPrincipalsByTextSortOrder},
		{"SearchPrincipalsByText_LimitClamped", testSearchPrincipalsByTextLimitClamped},
		{"SearchPrincipalsByText_NoMatch", testSearchPrincipalsByTextNoMatch},
		{"SearchPrincipalsByText_CaseInsensitive", testSearchPrincipalsByTextCaseInsensitive},
		// -- Directory/search domain-scoped search (REQ-JMAP-DIR-01) ------
		{"SearchPrincipalsByTextInDomain_EmptyDomain", testSearchPrincipalsByTextInDomainEmptyDomain},
		{"SearchPrincipalsByTextInDomain_DomainFilter", testSearchPrincipalsByTextInDomainDomainFilter},
		{"SearchPrincipalsByTextInDomain_CaseInsensitiveDomain", testSearchPrincipalsByTextInDomainCaseInsensitiveDomain},
		{"SearchPrincipalsByTextInDomain_NoMatch", testSearchPrincipalsByTextInDomainNoMatch},
		{"SearchPrincipalsByTextInDomain_EmptyPrefix", testSearchPrincipalsByTextInDomainEmptyPrefix},
		{"SearchPrincipalsByTextInDomain_LimitRespected", testSearchPrincipalsByTextInDomainLimitRespected},
		// -- REQ-MAIL-11e..m seen-addresses history -----------------------
		{"SeenAddress_UpsertInsert", testSeenAddressUpsertInsert},
		{"SeenAddress_UpsertUpdate_CountsIncrement", testSeenAddressUpsertUpdate},
		{"SeenAddress_Cap_501_OldestEvicted", testSeenAddressCap},
		{"SeenAddress_GetByEmail", testSeenAddressGetByEmail},
		{"SeenAddress_Destroy", testSeenAddressDestroy},
		{"SeenAddress_Purge", testSeenAddressPurge},
		// -- REQ-AUTH-EXT-SUBMIT-01..10 external SMTP submission ----------
		{"IdentitySubmission_MaterializeDefault_Idempotent", testMaterializeDefaultIdentity_Idempotent},
		{"IdentitySubmission_UpsertGet_Roundtrip", testIdentitySubmission_UpsertGet_Roundtrip},
		{"IdentitySubmission_OAuthFields_Roundtrip", testIdentitySubmission_OAuthFields_Roundtrip},
		{"IdentitySubmission_GetNotFound", testIdentitySubmission_GetNotFound},
		{"IdentitySubmission_StateTransition", testIdentitySubmission_StateTransition},
		{"IdentitySubmission_Delete_NotFoundAfter", testIdentitySubmission_Delete_NotFoundAfter},
		{"IdentitySubmission_Cascade_OnIdentityDelete", testIdentitySubmission_Cascade},
		{"IdentitySubmission_ListDue_OrderedAndFiltered", testIdentitySubmission_ListDue},
		{"IdentitySubmission_UpsertWithoutMaterialize_Errors", testIdentitySubmission_UpsertWithoutMaterialize},
		{"IdentitySubmission_CTValidation_ValidPrefix", testIdentitySubmission_CTValidation_ValidPrefix},
		{"IdentitySubmission_CTValidation_InvalidPrefix_Rejected", testIdentitySubmission_CTValidation_InvalidPrefix},
		{"IdentitySubmission_CTValidation_NilFields_Allowed", testIdentitySubmission_CTValidation_NilFields},
		{"IdentitySubmission_CountOAuth_AllRows", testIdentitySubmission_CountOAuth},
		// -- REQ-OPS-206/206a/219 clientlog ring buffer ------------------
		{"ClientLog_AppendAndList", testClientLogAppendAndList},
		{"ClientLog_NullableFields_PublicSlice", testClientLogNullableFieldsPublicSlice},
		{"ClientLog_Pagination", testClientLogPagination},
		{"ClientLog_ListByRequestID", testClientLogListByRequestID},
		{"ClientLog_EvictByAge", testClientLogEvictByAge},
		{"ClientLog_EvictByCap", testClientLogEvictByCap},
		{"ClientLog_EvictDoesNotCrossSlice", testClientLogEvictDoesNotCrossSlice},
		// -- REQ-OPS-208 / REQ-CLOG-06 session rows ----------------------
		{"Session_UpsertGet_Roundtrip", testSessionUpsertGetRoundtrip},
		{"Session_Upsert_UpdatesOnConflict", testSessionUpsertUpdatesOnConflict},
		{"Session_Get_NotFound", testSessionGetNotFound},
		{"Session_Delete_RemovesRow", testSessionDeleteRemovesRow},
		{"Session_Delete_NotFound", testSessionDeleteNotFound},
		{"Session_UpdateTelemetry_FlipsFlag", testSessionUpdateTelemetry},
		{"Session_UpdateTelemetry_NotFound", testSessionUpdateTelemetryNotFound},
		{"Session_EvictExpired_RemovesExpiredLeavesAlive", testSessionEvictExpired},
		{"Session_ClearExpiredLivetail", testSessionClearExpiredLivetail},
		{"Session_CascadeOnPrincipalDelete", testSessionCascadeOnPrincipalDelete},
	}
	for _, c := range cases {
		tc := c
		t.Run(tc.name, func(t *testing.T) {
			s, cleanup := f(t)
			t.Cleanup(cleanup)
			tc.fn(t, s)
		})
	}
}

// RunMigrationIdempotency is kept separate because it needs two Open
// calls against the same data directory. Backend tests pass a factory
// that re-opens a stable path; see storesqlite_test.go for the idiom.
func RunMigrationIdempotency(t *testing.T, openAgain func(t *testing.T) store.Store) {
	t.Helper()
	// The first Open in the caller already ran migrations; a second Open
	// must succeed and be a no-op.
	s := openAgain(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	// A trivial read must work after re-opening.
	if _, err := s.Meta().ListLocalDomains(ctx); err != nil {
		t.Fatalf("ListLocalDomains after reopen: %v", err)
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustInsertPrincipal(t *testing.T, s store.Store, email string) store.Principal {
	t.Helper()
	p, err := s.Meta().InsertPrincipal(ctxT(t), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
		DisplayName:    email,
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p
}

func mustInsertMailbox(t *testing.T, s store.Store, pID store.PrincipalID, name string) store.Mailbox {
	t.Helper()
	mb, err := s.Meta().InsertMailbox(ctxT(t), store.Mailbox{
		PrincipalID: pID,
		Name:        name,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(%s): %v", name, err)
	}
	return mb
}

func putBlob(t *testing.T, s store.Store, body string) store.BlobRef {
	t.Helper()
	ref, err := s.Blobs().Put(ctxT(t), strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	return ref
}

func testPrincipalsCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "alice@example.com")
	if p.ID == 0 {
		t.Fatalf("principal id unset")
	}
	if p.CreatedAt.IsZero() {
		t.Fatalf("principal CreatedAt zero")
	}
	got, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if got.CanonicalEmail != p.CanonicalEmail {
		t.Fatalf("GetPrincipalByID: got %q, want %q", got.CanonicalEmail, p.CanonicalEmail)
	}
	byEmail, err := s.Meta().GetPrincipalByEmail(ctx, p.CanonicalEmail)
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	if byEmail.ID != p.ID {
		t.Fatalf("GetPrincipalByEmail id = %d, want %d", byEmail.ID, p.ID)
	}
	_, err = s.Meta().GetPrincipalByEmail(ctx, "missing@example.com")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByEmail(absent) = %v, want ErrNotFound", err)
	}
	p.DisplayName = "Alice Updated"
	if err := s.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	got, err = s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID after update: %v", err)
	}
	if got.DisplayName != "Alice Updated" {
		t.Fatalf("DisplayName = %q, want %q", got.DisplayName, "Alice Updated")
	}
}

func testPrincipalConflict(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	_ = mustInsertPrincipal(t, s, "dup@example.com")
	_, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "dup@example.com",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate InsertPrincipal = %v, want ErrConflict", err)
	}
}

// driverLeakTokens lists substrings that must never appear in a conflict
// error message returned to callers. They are driver-internal identifiers
// that would leak storage implementation details into API responses.
var driverLeakTokens = []string{
	"(1555)",                     // SQLite extended error code
	"(2067)",                     // SQLite SQLITE_CONSTRAINT_UNIQUE
	"UNIQUE constraint",          // SQLite error text
	"duplicate key value",        // Postgres error text
	"violates unique",            // Postgres error text
	"domains_pkey",               // Postgres constraint name
	"principals_pkey",            // Postgres constraint name
	"mailboxes_pkey",             // Postgres constraint name
	"domains.name",               // SQLite column path
	"principals.canonical_email", // SQLite column path
}

func assertNoDriverLeak(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	for _, tok := range driverLeakTokens {
		if strings.Contains(msg, tok) {
			t.Errorf("conflict error leaks driver token %q: full error: %s", tok, msg)
		}
	}
}

func testPrincipalConflictErrorStringIsClean(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	_ = mustInsertPrincipal(t, s, "clean-dup@example.com")
	_, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "clean-dup@example.com",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertNoDriverLeak(t, err)
	// Must contain the entity identity so callers can log it.
	if !strings.Contains(err.Error(), "clean-dup@example.com") {
		t.Errorf("conflict error should mention the email address; got: %s", err.Error())
	}
}

func testDomainConflictErrorStringIsClean(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "clean-dup.example", IsLocal: true}); err != nil {
		t.Fatalf("first InsertDomain: %v", err)
	}
	err := s.Meta().InsertDomain(ctx, store.Domain{Name: "clean-dup.example", IsLocal: true})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertNoDriverLeak(t, err)
	if !strings.Contains(err.Error(), "clean-dup.example") {
		t.Errorf("conflict error should mention the domain name; got: %s", err.Error())
	}
}

func testDomainsCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate domain = %v, want ErrConflict", err)
	}
	d, err := s.Meta().GetDomain(ctx, "example.com")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if !d.IsLocal {
		t.Fatalf("GetDomain IsLocal = false, want true")
	}
	if _, err := s.Meta().GetDomain(ctx, "missing.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetDomain(absent) = %v", err)
	}
	ds, err := s.Meta().ListLocalDomains(ctx)
	if err != nil {
		t.Fatalf("ListLocalDomains: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("ListLocalDomains len = %d, want 1", len(ds))
	}
}

func testAliases(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "bob@example.com")
	a, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "bob.alias",
		Domain:          "example.com",
		TargetPrincipal: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if a.ID == 0 {
		t.Fatalf("alias id unset")
	}
	_, err = s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart:       "bob.alias",
		Domain:          "example.com",
		TargetPrincipal: p.ID,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate InsertAlias = %v, want ErrConflict", err)
	}
	got, err := s.Meta().ResolveAlias(ctx, "bob.alias", "example.com")
	if err != nil {
		t.Fatalf("ResolveAlias: %v", err)
	}
	if got != p.ID {
		t.Fatalf("ResolveAlias = %d, want %d", got, p.ID)
	}
	// Canonical address routes to the principal as well.
	got2, err := s.Meta().ResolveAlias(ctx, "bob", "example.com")
	if err != nil {
		t.Fatalf("ResolveAlias(canonical): %v", err)
	}
	if got2 != p.ID {
		t.Fatalf("ResolveAlias(canonical) = %d, want %d", got2, p.ID)
	}
	if _, err := s.Meta().ResolveAlias(ctx, "nobody", "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveAlias(absent) = %v", err)
	}
}

func testOIDC(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name:            "google",
		IssuerURL:       "https://accounts.google.com",
		ClientID:        "cid",
		ClientSecretRef: "file:/secret",
		Scopes:          []string{"openid", "email"},
		AutoProvision:   true,
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{Name: "google"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate OIDC = %v, want ErrConflict", err)
	}
	prov, err := s.Meta().GetOIDCProvider(ctx, "google")
	if err != nil {
		t.Fatalf("GetOIDCProvider: %v", err)
	}
	if prov.ClientID != "cid" {
		t.Fatalf("ClientID = %q", prov.ClientID)
	}
	if len(prov.Scopes) != 2 || prov.Scopes[0] != "openid" {
		t.Fatalf("Scopes = %v", prov.Scopes)
	}
	p := mustInsertPrincipal(t, s, "oidc@example.com")
	link := store.OIDCLink{
		PrincipalID:     p.ID,
		ProviderName:    "google",
		Subject:         "sub-123",
		EmailAtProvider: "oidc@example.com",
	}
	if err := s.Meta().LinkOIDC(ctx, link); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	if err := s.Meta().LinkOIDC(ctx, link); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate LinkOIDC = %v, want ErrConflict", err)
	}
	out, err := s.Meta().LookupOIDCLink(ctx, "google", "sub-123")
	if err != nil {
		t.Fatalf("LookupOIDCLink: %v", err)
	}
	if out.PrincipalID != p.ID {
		t.Fatalf("LookupOIDCLink principal = %d, want %d", out.PrincipalID, p.ID)
	}
	if _, err := s.Meta().LookupOIDCLink(ctx, "google", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LookupOIDCLink(absent) = %v", err)
	}
}

func testAPIKeys(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "apik@example.com")
	k, err := s.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        "h0",
		Name:        "test",
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if k.ID == 0 || k.CreatedAt.IsZero() {
		t.Fatalf("APIKey = %+v", k)
	}
	got, err := s.Meta().GetAPIKeyByHash(ctx, "h0")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if got.ID != k.ID {
		t.Fatalf("GetAPIKeyByHash id = %d", got.ID)
	}
	if _, err := s.Meta().GetAPIKeyByHash(ctx, "absent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAPIKeyByHash(absent) = %v", err)
	}
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().TouchAPIKey(ctx, k.ID, now); err != nil {
		t.Fatalf("TouchAPIKey: %v", err)
	}
	got, err = s.Meta().GetAPIKeyByHash(ctx, "h0")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash after touch: %v", err)
	}
	if !got.LastUsedAt.Equal(now) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, now)
	}
}

func testMailboxesCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mbox@example.com")
	inbox := mustInsertMailbox(t, s, p.ID, "INBOX")
	if inbox.ID == 0 || inbox.UIDValidity == 0 {
		t.Fatalf("mailbox missing ID/UIDValidity: %+v", inbox)
	}
	_ = mustInsertMailbox(t, s, p.ID, "Drafts")
	list, err := s.Meta().ListMailboxes(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListMailboxes len = %d", len(list))
	}
	if list[0].Name >= list[1].Name {
		t.Fatalf("ListMailboxes not sorted: %q, %q", list[0].Name, list[1].Name)
	}
	got, err := s.Meta().GetMailboxByID(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.Name != "INBOX" {
		t.Fatalf("GetMailboxByID name = %q", got.Name)
	}
}

func testMailboxConflict(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mbx-dup@example.com")
	_ = mustInsertMailbox(t, s, p.ID, "INBOX")
	_, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate InsertMailbox = %v", err)
	}
}

func testMailboxConflictErrorStringIsClean(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mbx-clean-dup@example.com")
	_ = mustInsertMailbox(t, s, p.ID, "CleanDupBox")
	_, err := s.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "CleanDupBox"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	assertNoDriverLeak(t, err)
	if !strings.Contains(err.Error(), "CleanDupBox") {
		t.Errorf("conflict error should mention the mailbox name; got: %s", err.Error())
	}
}

func testInsertMessageAllocatesUIDAndModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "msg@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "first")
	uid1, modseq1, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Blob:         ref,
		Size:         ref.Size,
		InternalDate: time.Unix(1000, 0).UTC(),
		ReceivedAt:   time.Unix(1000, 0).UTC(),
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage #1: %v", err)
	}
	if uid1 != 1 {
		t.Fatalf("first UID = %d, want 1", uid1)
	}
	ref2 := putBlob(t, s, "second")
	uid2, modseq2, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Blob:         ref2,
		Size:         ref2.Size,
		InternalDate: time.Unix(1001, 0).UTC(),
		ReceivedAt:   time.Unix(1001, 0).UTC(),
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("InsertMessage #2: %v", err)
	}
	if uid2 != 2 {
		t.Fatalf("second UID = %d, want 2", uid2)
	}
	if modseq2 <= modseq1 {
		t.Fatalf("ModSeq not monotonic: %d then %d", modseq1, modseq2)
	}
	got, err := s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.UIDNext != 3 {
		t.Fatalf("UIDNext = %d, want 3", got.UIDNext)
	}
	if got.HighestModSeq != modseq2 {
		t.Fatalf("HighestModSeq = %d, want %d", got.HighestModSeq, modseq2)
	}
}

func testUpdateFlagsBumpsModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "flags@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "body-a")
	_, modseq0, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Find the message id by listing (simple scan via expunge semantics
	// isn't exposed here; we rely on InsertMessage returning UID=1 and
	// assume id is discoverable via a Get by (mailbox, uid). The
	// interface does not currently expose such a method, so this test
	// uses a fresh message we know the ID of via round-trip.
	// Workaround: InsertMessage returns uid, modseq; we need the id.
	// The interface doesn't return it directly, so we use a helper that
	// enumerates messages via the change feed to discover ids.
	id := firstMessageIDFromFeed(t, s, p.ID)
	modseq1, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, store.MessageFlagSeen, 0, nil, nil, 0)
	if err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}
	if modseq1 <= modseq0 {
		t.Fatalf("flag update did not bump ModSeq: %d then %d", modseq0, modseq1)
	}
	got, err := s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("Flags missing \\Seen after add")
	}
	if got.ModSeq != modseq1 {
		t.Fatalf("GetMessage ModSeq = %d, want %d", got.ModSeq, modseq1)
	}
	// Clear the flag.
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, 0, store.MessageFlagSeen, nil, nil, 0); err != nil {
		t.Fatalf("clear \\Seen: %v", err)
	}
	got, err = s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage after clear: %v", err)
	}
	if got.Flags&store.MessageFlagSeen != 0 {
		t.Fatalf("Flags still has \\Seen after clear")
	}
}

func testUpdateFlagsUnchangedSince(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ucs@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "ucs-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	modseq, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, store.MessageFlagFlagged, 0, nil, nil, 0)
	if err != nil {
		t.Fatalf("initial update: %v", err)
	}
	// unchangedSince with stale ModSeq must conflict.
	_, err = s.Meta().UpdateMessageFlags(ctx, id, mb.ID, store.MessageFlagSeen, 0, nil, nil, modseq-1)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale UNCHANGEDSINCE = %v, want ErrConflict", err)
	}
	// unchangedSince == current ModSeq is allowed.
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, store.MessageFlagSeen, 0, nil, nil, modseq); err != nil {
		t.Fatalf("current UNCHANGEDSINCE: %v", err)
	}
}

func testUpdateFlagsKeywords(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "kw@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "kw-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, 0, 0, []string{"work", "urgent"}, nil, 0); err != nil {
		t.Fatalf("add keywords: %v", err)
	}
	got, err := s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !containsAll(got.Keywords, []string{"work", "urgent"}) {
		t.Fatalf("Keywords = %v, want work+urgent", got.Keywords)
	}
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, 0, 0, nil, []string{"urgent"}, 0); err != nil {
		t.Fatalf("remove keyword: %v", err)
	}
	got, err = s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage after remove: %v", err)
	}
	if containsAll(got.Keywords, []string{"urgent"}) {
		t.Fatalf("Keywords still has urgent: %v", got.Keywords)
	}
	if !containsAll(got.Keywords, []string{"work"}) {
		t.Fatalf("Keywords missing work: %v", got.Keywords)
	}
}

func testExpungeMessages(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "exp@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	var ids []store.MessageID
	for i := 0; i < 3; i++ {
		ref := putBlob(t, s, "exp-"+string(rune('a'+i)))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	// Gather all three ids from the feed (there is no ListMessages in
	// the interface yet).
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, c := range feed {
		if c.Kind == store.EntityKindEmail && c.Op == store.ChangeOpCreated && c.EntityID != 0 {
			ids = append(ids, store.MessageID(c.EntityID))
		}
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 created messages in feed, got %d", len(ids))
	}
	if err := s.Meta().ExpungeMessages(ctx, mb.ID, ids[:2]); err != nil {
		t.Fatalf("Expunge: %v", err)
	}
	// First two gone, last remains.
	if _, err := s.Meta().GetMessage(ctx, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage[0] after expunge = %v", err)
	}
	if _, err := s.Meta().GetMessage(ctx, ids[2]); err != nil {
		t.Fatalf("GetMessage[2] after expunge: %v", err)
	}
	// Re-expunging already-gone ids is a silent success unless all gone.
	if err := s.Meta().ExpungeMessages(ctx, mb.ID, []store.MessageID{ids[0], ids[2]}); err != nil {
		t.Fatalf("Expunge mixed: %v", err)
	}
	// Expunging only already-gone returns ErrNotFound.
	if err := s.Meta().ExpungeMessages(ctx, mb.ID, []store.MessageID{ids[0]}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Expunge all-gone = %v, want ErrNotFound", err)
	}
}

func testChangeFeedMonotonic(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "feed@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	var lastSeq store.ChangeSeq
	for i := 0; i < 5; i++ {
		ref := putBlob(t, s, "feed-"+string(rune('0'+i)))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	if len(feed) < 6 { // 1 mailbox + 5 messages
		t.Fatalf("feed len = %d, want >= 6", len(feed))
	}
	for i, c := range feed {
		if c.Seq <= lastSeq && i != 0 {
			t.Fatalf("feed not monotonic at %d: prev %d, got %d", i, lastSeq, c.Seq)
		}
		if c.PrincipalID != p.ID {
			t.Fatalf("feed entry principal = %d, want %d", c.PrincipalID, p.ID)
		}
		lastSeq = c.Seq
	}
	// Paginated read must skip entries with Seq <= fromSeq.
	cursor := feed[2].Seq
	rest, err := s.Meta().ReadChangeFeed(ctx, p.ID, cursor, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed paginated: %v", err)
	}
	if len(rest)+3 != len(feed) {
		t.Fatalf("paginated feed len = %d, want %d", len(rest), len(feed)-3)
	}
	for _, c := range rest {
		if c.Seq <= cursor {
			t.Fatalf("paginated feed includes Seq %d <= cursor %d", c.Seq, cursor)
		}
	}
}

func testQuotaEnforcement(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "quota@example.com",
		QuotaBytes:     10, // tight
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	small := putBlob(t, s, "1234567")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: small, Size: small.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert small: %v", err)
	}
	big := putBlob(t, s, "aaaaaaaaaaaaaaaaaaaaa")
	_, _, err = s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: big, Size: big.Size}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("Insert over quota = %v, want ErrQuotaExceeded", err)
	}
}

func testDeleteMailboxCascades(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "del@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "Temp")
	ref := putBlob(t, s, "to-delete")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteMailbox(ctx, mb.ID); err != nil {
		t.Fatalf("DeleteMailbox: %v", err)
	}
	if _, err := s.Meta().GetMailboxByID(ctx, mb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID after delete = %v", err)
	}
	if err := s.Meta().DeleteMailbox(ctx, mb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteMailbox(absent) = %v", err)
	}
}

func testBlobRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	body := []byte("hello blob world")
	ref, err := s.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Fatalf("Size = %d", ref.Size)
	}
	r, err := s.Blobs().Get(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch")
	}
	size, refs, err := s.Blobs().Stat(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(body)) || refs < 1 {
		t.Fatalf("Stat = (size=%d, refs=%d)", size, refs)
	}
}

func testBlobDedup(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	body := []byte("dedup me")
	a, err := s.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	b, err := s.Blobs().Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if a != b {
		t.Fatalf("Put not dedup: %+v vs %+v", a, b)
	}
}

func testBlobNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	_, err := s.Blobs().Get(ctx, strings.Repeat("0", 64))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(absent) = %v, want ErrNotFound", err)
	}
	if err := s.Blobs().Delete(ctx, strings.Repeat("0", 64)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete(absent) = %v, want ErrNotFound", err)
	}
}

func testFTSSmoke(t *testing.T, s store.Store) {
	// The FTS backend in storesqlite/storepg is a trivial stub in Wave
	// 1; the real Bleve-backed index ships as internal/storefts. We only
	// assert the surface compiles and accepts calls without error on an
	// empty index.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "fts@example.com")
	_, err := s.FTS().Query(ctx, p.ID, store.Query{Text: "anything"})
	if err != nil {
		// Some backends return a "not implemented" error, which is also
		// acceptable. We accept any outcome that isn't a panic.
		t.Logf("FTS Query on empty index returned: %v", err)
	}
	// RemoveMessage of absent id must not explode.
	if err := s.FTS().RemoveMessage(ctx, store.MessageID(9999)); err != nil {
		t.Logf("FTS RemoveMessage returned: %v", err)
	}
	// Commit on empty batch must be a no-op.
	if err := s.FTS().Commit(ctx); err != nil {
		t.Logf("FTS Commit returned: %v", err)
	}
}

// firstMessageIDFromFeed finds the MessageID of the first email-created
// entry (Kind == EntityKindEmail, Op == ChangeOpCreated) in the
// principal's change feed. Helper used by tests that need a MessageID
// but InsertMessage does not return one (the interface returns UID +
// ModSeq; id discovery is via the feed or a later ListMessages method).
func firstMessageIDFromFeed(t *testing.T, s store.Store, principalID store.PrincipalID) store.MessageID {
	t.Helper()
	feed, err := s.Meta().ReadChangeFeed(ctxT(t), principalID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, c := range feed {
		if c.Kind == store.EntityKindEmail && c.Op == store.ChangeOpCreated && c.EntityID != 0 {
			return store.MessageID(c.EntityID)
		}
	}
	t.Fatalf("no email-created entry in feed")
	return 0
}

func containsAll(haystack, needles []string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// -- Wave 2 compliance cases ------------------------------------------

func testFTSCursorEmptyIsZero(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	seq, err := s.Meta().GetFTSCursor(ctx, "fts")
	if err != nil {
		t.Fatalf("GetFTSCursor(absent): %v", err)
	}
	if seq != 0 {
		t.Fatalf("absent cursor = %d, want 0", seq)
	}
}

func testFTSCursorUpsertRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().SetFTSCursor(ctx, "fts", 42); err != nil {
		t.Fatalf("SetFTSCursor: %v", err)
	}
	got, err := s.Meta().GetFTSCursor(ctx, "fts")
	if err != nil {
		t.Fatalf("GetFTSCursor: %v", err)
	}
	if got != 42 {
		t.Fatalf("cursor = %d, want 42", got)
	}
	// Upsert (advance).
	if err := s.Meta().SetFTSCursor(ctx, "fts", 100); err != nil {
		t.Fatalf("SetFTSCursor advance: %v", err)
	}
	got, err = s.Meta().GetFTSCursor(ctx, "fts")
	if err != nil {
		t.Fatalf("GetFTSCursor after advance: %v", err)
	}
	if got != 100 {
		t.Fatalf("cursor after advance = %d, want 100", got)
	}
	// Idempotent re-apply.
	if err := s.Meta().SetFTSCursor(ctx, "fts", 100); err != nil {
		t.Fatalf("SetFTSCursor idempotent: %v", err)
	}
}

// testFTSCursorConcurrent drives a handful of parallel SetFTSCursor
// calls on different keys and asserts they all land without data race
// or interleaving corruption. Run under -race in CI.
func testFTSCursorConcurrent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	keys := []string{"a", "b", "c", "d", "e"}
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func(k string, v uint64) {
			defer wg.Done()
			if err := s.Meta().SetFTSCursor(ctx, k, v); err != nil {
				t.Errorf("SetFTSCursor(%s): %v", k, err)
			}
		}(k, uint64(i+1))
	}
	wg.Wait()
	for i, k := range keys {
		got, err := s.Meta().GetFTSCursor(ctx, k)
		if err != nil {
			t.Fatalf("GetFTSCursor(%s): %v", k, err)
		}
		if got != uint64(i+1) {
			t.Fatalf("cursor[%s] = %d, want %d", k, got, i+1)
		}
	}
}

func testDeletePrincipalCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cascade@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "cascade-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "cascade.alias", Domain: "example.com", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "casc-provider", IssuerURL: "https://example.com", ClientID: "cid",
		ClientSecretRef: "x",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "casc-provider", Subject: "sub-c",
	}); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	if _, err := s.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID, Hash: "casc-h", Name: "t",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if err := s.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        time.Now().UTC(),
		ActorKind: store.ActorPrincipal,
		ActorID:   "1",
		Action:    "principal.login",
		Subject:   subjectPrincipal(p.ID),
		Outcome:   store.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}

	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}

	// Principal row gone.
	if _, err := s.Meta().GetPrincipalByID(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPrincipalByID after delete = %v, want ErrNotFound", err)
	}
	// Mailbox gone.
	if _, err := s.Meta().GetMailboxByID(ctx, mb.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByID after delete = %v", err)
	}
	// Alias gone.
	if _, err := s.Meta().ResolveAlias(ctx, "cascade.alias", "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveAlias after delete = %v", err)
	}
	// OIDC link gone.
	if _, err := s.Meta().LookupOIDCLink(ctx, "casc-provider", "sub-c"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LookupOIDCLink after delete = %v", err)
	}
	// API key gone.
	if _, err := s.Meta().GetAPIKeyByHash(ctx, "casc-h"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAPIKeyByHash after delete = %v", err)
	}
	// Audit entries for this principal gone.
	entries, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{PrincipalID: p.ID})
	if err != nil {
		t.Fatalf("ListAuditLog after delete: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("audit entries after delete = %d, want 0", len(entries))
	}
	// Second delete -> ErrNotFound.
	if err := s.Meta().DeletePrincipal(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeletePrincipal(gone) = %v", err)
	}
}

func testListPrincipalsKeyset(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	const n = 50
	ids := make([]store.PrincipalID, 0, n)
	for i := 0; i < n; i++ {
		p := mustInsertPrincipal(t, s, fmt.Sprintf("kp-%02d@example.com", i))
		ids = append(ids, p.ID)
	}
	// Page through with limit=20; each principal must appear exactly
	// once, in ascending ID order.
	seen := make(map[store.PrincipalID]struct{}, n)
	var after store.PrincipalID
	var prev store.PrincipalID
	for {
		page, err := s.Meta().ListPrincipals(ctx, after, 20)
		if err != nil {
			t.Fatalf("ListPrincipals(after=%d): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		for _, p := range page {
			if p.ID <= prev {
				t.Fatalf("ListPrincipals not sorted asc: prev=%d curr=%d", prev, p.ID)
			}
			prev = p.ID
			if _, dup := seen[p.ID]; dup {
				t.Fatalf("duplicate id %d", p.ID)
			}
			seen[p.ID] = struct{}{}
		}
		after = page[len(page)-1].ID
	}
	// Only our seeded principals should appear (the factory hands us a
	// fresh store per sub-test).
	if len(seen) < n {
		t.Fatalf("saw %d principals, want >= %d", len(seen), n)
	}
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing principal id %d", id)
		}
	}
}

func testDeleteOIDCProviderCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "dp1@example.com")
	p2 := mustInsertPrincipal(t, s, "dp2@example.com")
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "todelete", IssuerURL: "https://i.test", ClientID: "c",
		ClientSecretRef: "x",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	for i, pid := range []store.PrincipalID{p1.ID, p2.ID} {
		if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
			PrincipalID: pid, ProviderName: "todelete",
			Subject: fmt.Sprintf("sub-%d", i),
		}); err != nil {
			t.Fatalf("LinkOIDC: %v", err)
		}
	}
	if err := s.Meta().DeleteOIDCProvider(ctx, store.OIDCProviderID("todelete")); err != nil {
		t.Fatalf("DeleteOIDCProvider: %v", err)
	}
	// Provider gone.
	if _, err := s.Meta().GetOIDCProvider(ctx, "todelete"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetOIDCProvider after delete = %v", err)
	}
	// Links gone.
	for i := 0; i < 2; i++ {
		if _, err := s.Meta().LookupOIDCLink(ctx, "todelete", fmt.Sprintf("sub-%d", i)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("link %d present after provider delete", i)
		}
	}
	// Principals survive.
	if _, err := s.Meta().GetPrincipalByID(ctx, p1.ID); err != nil {
		t.Fatalf("principal 1 vanished: %v", err)
	}
	if _, err := s.Meta().GetPrincipalByID(ctx, p2.ID); err != nil {
		t.Fatalf("principal 2 vanished: %v", err)
	}
	// Second delete -> ErrNotFound.
	if err := s.Meta().DeleteOIDCProvider(ctx, store.OIDCProviderID("todelete")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteOIDCProvider(gone) = %v", err)
	}
}

func testListOIDCProviders(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	names := []string{"aaa", "bbb", "ccc"}
	for _, n := range names {
		if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
			Name: n, IssuerURL: "https://" + n + ".test", ClientID: "c",
			ClientSecretRef: "x",
		}); err != nil {
			t.Fatalf("InsertOIDCProvider(%s): %v", n, err)
		}
	}
	out, err := s.Meta().ListOIDCProviders(ctx)
	if err != nil {
		t.Fatalf("ListOIDCProviders: %v", err)
	}
	if len(out) < len(names) {
		t.Fatalf("len=%d, want >= %d", len(out), len(names))
	}
	// Every seeded name must appear.
	for _, want := range names {
		found := false
		for _, got := range out {
			if got.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing provider %q in list", want)
		}
	}
}

func testUnlinkOIDCNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "unlink@example.com")
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "pv-unlink", IssuerURL: "https://u.test", ClientID: "c",
		ClientSecretRef: "x",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider: %v", err)
	}
	// Absent -> ErrNotFound.
	if err := s.Meta().UnlinkOIDC(ctx, p.ID, store.OIDCProviderID("pv-unlink")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UnlinkOIDC(absent) = %v", err)
	}
	// Round-trip: link then unlink.
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "pv-unlink", Subject: "sub-u",
	}); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	if err := s.Meta().UnlinkOIDC(ctx, p.ID, store.OIDCProviderID("pv-unlink")); err != nil {
		t.Fatalf("UnlinkOIDC: %v", err)
	}
	// Now it's gone.
	if _, err := s.Meta().LookupOIDCLink(ctx, "pv-unlink", "sub-u"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link still present after unlink")
	}
}

func testAuditLogAppendAndList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "al1@example.com")
	p2 := mustInsertPrincipal(t, s, "al2@example.com")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	actions := []string{"principal.login", "principal.password.change", "totp.enroll"}
	for i := 0; i < 12; i++ {
		pid := p1.ID
		if i%2 == 1 {
			pid = p2.ID
		}
		if err := s.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:        base.Add(time.Duration(i) * time.Minute),
			ActorKind: store.ActorPrincipal,
			ActorID:   fmt.Sprintf("%d", pid),
			Action:    actions[i%len(actions)],
			Subject:   subjectPrincipal(pid),
			Outcome:   store.OutcomeSuccess,
			Metadata:  map[string]string{"idx": fmt.Sprintf("%d", i)},
		}); err != nil {
			t.Fatalf("AppendAuditLog %d: %v", i, err)
		}
	}

	// All.
	all, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{})
	if err != nil {
		t.Fatalf("ListAuditLog(all): %v", err)
	}
	if len(all) < 12 {
		t.Fatalf("audit all len = %d, want >= 12", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("audit list not ordered: %d then %d", all[i-1].ID, all[i].ID)
		}
	}
	// Filter by principal.
	byP1, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{PrincipalID: p1.ID})
	if err != nil {
		t.Fatalf("ListAuditLog(p1): %v", err)
	}
	if len(byP1) != 6 {
		t.Fatalf("p1 entries = %d, want 6", len(byP1))
	}
	for _, e := range byP1 {
		if e.Subject != subjectPrincipal(p1.ID) {
			t.Fatalf("unexpected subject %q in p1 filter", e.Subject)
		}
	}
	// Filter by action.
	byAction, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{Action: "totp.enroll"})
	if err != nil {
		t.Fatalf("ListAuditLog(action): %v", err)
	}
	if len(byAction) != 4 {
		t.Fatalf("action entries = %d, want 4", len(byAction))
	}
	// Filter by time range: minutes [3, 6) -> minutes 3, 4, 5 = 3 rows.
	byTime, err := s.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		Since: base.Add(3 * time.Minute),
		Until: base.Add(6 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ListAuditLog(time): %v", err)
	}
	if len(byTime) != 3 {
		t.Fatalf("time entries = %d, want 3", len(byTime))
	}
	// Verify metadata round-trips.
	first := byTime[0]
	if first.Metadata["idx"] == "" {
		t.Fatalf("metadata idx empty after roundtrip: %+v", first.Metadata)
	}
}

func testPrincipalFlagTOTPEnabled(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "totp-flag@example.com")
	if p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("new principal has TOTPEnabled flag set")
	}
	p.Flags |= store.PrincipalFlagTOTPEnabled
	if err := s.Meta().UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal set: %v", err)
	}
	got, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after set: %v", err)
	}
	if !got.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("flag did not persist: %b", got.Flags)
	}
	got.Flags &^= store.PrincipalFlagTOTPEnabled
	if err := s.Meta().UpdatePrincipal(ctx, got); err != nil {
		t.Fatalf("UpdatePrincipal clear: %v", err)
	}
	got2, err := s.Meta().GetPrincipalByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if got2.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Fatalf("flag did not clear: %b", got2.Flags)
	}
}

func subjectPrincipal(pid store.PrincipalID) string {
	return fmt.Sprintf("principal:%d", pid)
}

// -- Wave 3 IMAP mailbox surface + Sieve scripts ---------------------

func testGetMailboxByName(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "gmbn@example.com")
	inbox := mustInsertMailbox(t, s, p.ID, "INBOX")
	_ = mustInsertMailbox(t, s, p.ID, "Archive")
	got, err := s.Meta().GetMailboxByName(ctx, p.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	if got.ID != inbox.ID {
		t.Fatalf("got id %d, want %d", got.ID, inbox.ID)
	}
	// Case-sensitive: "inbox" lowercase must not match INBOX.
	if _, err := s.Meta().GetMailboxByName(ctx, p.ID, "inbox"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("case-sensitive lookup = %v, want ErrNotFound", err)
	}
	// Different principal isolation.
	other := mustInsertPrincipal(t, s, "gmbn-other@example.com")
	if _, err := s.Meta().GetMailboxByName(ctx, other.ID, "INBOX"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-principal leak = %v, want ErrNotFound", err)
	}
}

func testListMessagesPagination(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "lmp@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	const n = 5
	for i := 0; i < n; i++ {
		ref := putBlob(t, s, fmt.Sprintf("lmp-%d", i))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
			PrincipalID: p.ID, Blob: ref, Size: ref.Size,
		}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("InsertMessage %d: %v", i, err)
		}
	}
	// Empty mailbox returns empty slice.
	other := mustInsertMailbox(t, s, p.ID, "Empty")
	empty, err := s.Meta().ListMessages(ctx, other.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty mailbox len = %d, want 0", len(empty))
	}
	// Full page, UID-ascending.
	msgs, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("full list len = %d, want %d", len(msgs), n)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].UID >= msgs[i].UID {
			t.Fatalf("not UID-ascending at %d: %d then %d", i, msgs[i-1].UID, msgs[i].UID)
		}
	}
	// AfterUID cursor skips earlier UIDs.
	rest, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{AfterUID: msgs[1].UID})
	if err != nil {
		t.Fatalf("ListMessages(AfterUID): %v", err)
	}
	if len(rest) != n-2 {
		t.Fatalf("after-UID len = %d, want %d", len(rest), n-2)
	}
	for _, m := range rest {
		if m.UID <= msgs[1].UID {
			t.Fatalf("page includes UID %d <= cursor %d", m.UID, msgs[1].UID)
		}
	}
	// Limit caps the page.
	capped, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListMessages(Limit): %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("limited page len = %d, want 2", len(capped))
	}
}

// testListMessagesReceivedBefore exercises the ReceivedBefore filter added
// for the trash retention sweeper (REQ-STORE-90). Two messages are inserted
// with explicitly controlled InternalDate values — one 31 days old, one 1 day
// old — and the test asserts that only the aged-out row appears when the
// filter cutoff is set to 30 days ago.
func testListMessagesReceivedBefore(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "trash-filter@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "Trash")
	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour)
	recent := now.Add(-1 * 24 * time.Hour)
	// Insert the old message.
	oldBlob := putBlob(t, s, "old-trash-message")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Blob:         oldBlob,
		Size:         oldBlob.Size,
		InternalDate: old,
		ReceivedAt:   old,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage old: %v", err)
	}
	// Insert the recent message.
	recentBlob := putBlob(t, s, "recent-trash-message")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Blob:         recentBlob,
		Size:         recentBlob.Size,
		InternalDate: recent,
		ReceivedAt:   recent,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage recent: %v", err)
	}
	// No filter: both messages are visible.
	all, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages (no filter): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("no-filter len = %d, want 2", len(all))
	}
	// ReceivedBefore = 30 days ago: only the 31-day-old message is returned.
	cutoff := now.Add(-30 * 24 * time.Hour)
	aged, err := s.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{ReceivedBefore: &cutoff})
	if err != nil {
		t.Fatalf("ListMessages (ReceivedBefore): %v", err)
	}
	if len(aged) != 1 {
		t.Fatalf("ReceivedBefore len = %d, want 1 (only the 31-day-old message)", len(aged))
	}
	if !aged[0].InternalDate.Before(cutoff) {
		t.Fatalf("returned message InternalDate %v is not before cutoff %v",
			aged[0].InternalDate, cutoff)
	}
}

func testSetMailboxSubscribed(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	// Initial mailbox has no subscribed bit.
	if mb.Attributes&store.MailboxAttrSubscribed != 0 {
		t.Fatalf("fresh mailbox already subscribed: %b", mb.Attributes)
	}
	if err := s.Meta().SetMailboxSubscribed(ctx, mb.ID, true); err != nil {
		t.Fatalf("SetMailboxSubscribed(true): %v", err)
	}
	got, err := s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.Attributes&store.MailboxAttrSubscribed == 0 {
		t.Fatalf("subscribed bit did not set: %b", got.Attributes)
	}
	if err := s.Meta().SetMailboxSubscribed(ctx, mb.ID, false); err != nil {
		t.Fatalf("SetMailboxSubscribed(false): %v", err)
	}
	got, err = s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID after unsubscribe: %v", err)
	}
	if got.Attributes&store.MailboxAttrSubscribed != 0 {
		t.Fatalf("subscribed bit did not clear: %b", got.Attributes)
	}
	// Absent mailbox -> ErrNotFound.
	if err := s.Meta().SetMailboxSubscribed(ctx, store.MailboxID(99999), true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetMailboxSubscribed(absent) = %v, want ErrNotFound", err)
	}
}

func testRenameMailbox(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "rnm@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "OldName")
	_ = mustInsertMailbox(t, s, p.ID, "TakenName")
	if err := s.Meta().RenameMailbox(ctx, mb.ID, "NewName"); err != nil {
		t.Fatalf("RenameMailbox: %v", err)
	}
	if _, err := s.Meta().GetMailboxByName(ctx, p.ID, "OldName"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old name still present: %v", err)
	}
	got, err := s.Meta().GetMailboxByName(ctx, p.ID, "NewName")
	if err != nil {
		t.Fatalf("GetMailboxByName(NewName): %v", err)
	}
	if got.ID != mb.ID {
		t.Fatalf("renamed id = %d, want %d", got.ID, mb.ID)
	}
	// Collision with existing name -> ErrConflict.
	if err := s.Meta().RenameMailbox(ctx, mb.ID, "TakenName"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("RenameMailbox(collision) = %v, want ErrConflict", err)
	}
	// Absent mailbox -> ErrNotFound.
	if err := s.Meta().RenameMailbox(ctx, store.MailboxID(99999), "Whatever"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RenameMailbox(absent) = %v, want ErrNotFound", err)
	}
}

func testSieveScriptEmpty(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sse@example.com")
	got, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript(empty): %v", err)
	}
	if got != "" {
		t.Fatalf("empty script = %q, want \"\"", got)
	}
}

func testSieveScriptRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ssr@example.com")
	const script = `require "fileinto"; fileinto "INBOX.Hello";`
	if err := s.Meta().SetSieveScript(ctx, p.ID, script); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}
	got, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if got != script {
		t.Fatalf("script = %q, want %q", got, script)
	}
	// Setting empty text removes the script.
	if err := s.Meta().SetSieveScript(ctx, p.ID, ""); err != nil {
		t.Fatalf("SetSieveScript(empty): %v", err)
	}
	after, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript after delete: %v", err)
	}
	if after != "" {
		t.Fatalf("after delete = %q, want empty", after)
	}
}

func testSieveScriptOverwrite(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sso@example.com")
	const first = `keep;`
	const second = `require "fileinto"; fileinto "Updated";`
	if err := s.Meta().SetSieveScript(ctx, p.ID, first); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := s.Meta().SetSieveScript(ctx, p.ID, second); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, err := s.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != second {
		t.Fatalf("overwrite = %q, want %q", got, second)
	}
}

func testSieveScriptCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ssc@example.com")
	if err := s.Meta().SetSieveScript(ctx, p.ID, `keep;`); err != nil {
		t.Fatalf("SetSieveScript: %v", err)
	}
	// Give the principal at least one mailbox/message so DeletePrincipal
	// exercises the full cascade path.
	_ = mustInsertMailbox(t, s, p.ID, "INBOX")
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	// Recreate the principal; the script must not come back.
	p2 := mustInsertPrincipal(t, s, "ssc@example.com")
	got, err := s.Meta().GetSieveScript(ctx, p2.ID)
	if err != nil {
		t.Fatalf("GetSieveScript after recreate: %v", err)
	}
	if got != "" {
		t.Fatalf("script survived DeletePrincipal: %q", got)
	}
}

// -- Wave 3 admin REST gap-closer methods -----------------------------

func testListAliasesByDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "aliaslist@example.com")
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "a", Domain: "example.com", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias a: %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "b", Domain: "example.com", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias b: %v", err)
	}
	if _, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "c", Domain: "other.test", TargetPrincipal: p.ID,
	}); err != nil {
		t.Fatalf("InsertAlias c: %v", err)
	}
	all, err := s.Meta().ListAliases(ctx, "")
	if err != nil {
		t.Fatalf("ListAliases all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAliases all = %d, want 3", len(all))
	}
	scoped, err := s.Meta().ListAliases(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListAliases example.com: %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("ListAliases example.com = %d, want 2", len(scoped))
	}
	for _, a := range scoped {
		if a.Domain != "example.com" {
			t.Fatalf("filter leaked: %+v", a)
		}
	}
}

func testDeleteAliasNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().DeleteAlias(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteAlias(absent) = %v, want ErrNotFound", err)
	}
	p := mustInsertPrincipal(t, s, "aliasdel@example.com")
	a, err := s.Meta().InsertAlias(ctx, store.Alias{
		LocalPart: "ad", Domain: "example.com", TargetPrincipal: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertAlias: %v", err)
	}
	if err := s.Meta().DeleteAlias(ctx, a.ID); err != nil {
		t.Fatalf("DeleteAlias: %v", err)
	}
	if _, err := s.Meta().ResolveAlias(ctx, "ad", "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ResolveAlias after delete = %v, want ErrNotFound", err)
	}
}

func testDeleteDomainNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().DeleteDomain(ctx, "absent.example"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteDomain(absent) = %v, want ErrNotFound", err)
	}
	if err := s.Meta().InsertDomain(ctx, store.Domain{Name: "del.example", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	if err := s.Meta().DeleteDomain(ctx, "DEL.EXAMPLE"); err != nil {
		t.Fatalf("DeleteDomain case: %v", err)
	}
	if _, err := s.Meta().GetDomain(ctx, "del.example"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetDomain after delete = %v", err)
	}
}

func testListAPIKeysByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "keys@example.com")
	p2 := mustInsertPrincipal(t, s, "keys2@example.com")
	k1, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p.ID, Hash: "kh1", Name: "k1"})
	if err != nil {
		t.Fatalf("InsertAPIKey 1: %v", err)
	}
	k2, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p.ID, Hash: "kh2", Name: "k2"})
	if err != nil {
		t.Fatalf("InsertAPIKey 2: %v", err)
	}
	if _, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p2.ID, Hash: "kh3", Name: "k3"}); err != nil {
		t.Fatalf("InsertAPIKey 3: %v", err)
	}
	list, err := s.Meta().ListAPIKeysByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAPIKeysByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListAPIKeysByPrincipal = %d, want 2", len(list))
	}
	if list[0].ID != k1.ID || list[1].ID != k2.ID {
		t.Fatalf("list order: %+v %+v", list[0], list[1])
	}
}

func testDeleteAPIKeyNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().DeleteAPIKey(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteAPIKey(absent) = %v, want ErrNotFound", err)
	}
	p := mustInsertPrincipal(t, s, "keydel@example.com")
	k, err := s.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: p.ID, Hash: "zz1", Name: "z"})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if err := s.Meta().DeleteAPIKey(ctx, k.ID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	if _, err := s.Meta().GetAPIKeyByHash(ctx, "zz1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAPIKeyByHash after delete = %v", err)
	}
}

func testListOIDCLinksByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "prov1", IssuerURL: "https://issuer1", ClientID: "c1",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider 1: %v", err)
	}
	if err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
		Name: "prov2", IssuerURL: "https://issuer2", ClientID: "c2",
	}); err != nil {
		t.Fatalf("InsertOIDCProvider 2: %v", err)
	}
	p := mustInsertPrincipal(t, s, "oidclist@example.com")
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "prov1", Subject: "sub-1",
	}); err != nil {
		t.Fatalf("LinkOIDC 1: %v", err)
	}
	if err := s.Meta().LinkOIDC(ctx, store.OIDCLink{
		PrincipalID: p.ID, ProviderName: "prov2", Subject: "sub-2",
	}); err != nil {
		t.Fatalf("LinkOIDC 2: %v", err)
	}
	list, err := s.Meta().ListOIDCLinksByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListOIDCLinksByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListOIDCLinksByPrincipal = %d, want 2", len(list))
	}
	if list[0].ProviderName != "prov1" || list[1].ProviderName != "prov2" {
		t.Fatalf("ordering: %+v", list)
	}
}

// -- Phase 2 Wave 2.0 compliance cases -------------------------------

func mustEnqueue(t *testing.T, s store.Store, item store.QueueItem) store.QueueItemID {
	t.Helper()
	if item.BodyBlobHash == "" {
		ref := putBlob(t, s, "queue-body-"+item.RcptTo)
		item.BodyBlobHash = ref.Hash
	}
	id, err := s.Meta().EnqueueMessage(ctxT(t), item)
	if err != nil {
		t.Fatalf("EnqueueMessage(%s): %v", item.RcptTo, err)
	}
	return id
}

func testQueueEnqueueAndList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-list@example.com")
	id1 := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID,
		MailFrom:    "alice@example.com",
		RcptTo:      "bob@dest.test",
		EnvelopeID:  "env-1",
	})
	id2 := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID,
		MailFrom:    "alice@example.com",
		RcptTo:      "carol@dest.test",
		EnvelopeID:  "env-1",
	})
	if id1 == 0 || id2 == 0 || id1 == id2 {
		t.Fatalf("EnqueueMessage ids: %d %d", id1, id2)
	}
	got, err := s.Meta().GetQueueItem(ctx, id1)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if got.RcptTo != "bob@dest.test" || got.State != store.QueueStateQueued {
		t.Fatalf("queue row mismatch: %+v", got)
	}
	all, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("queue len = %d, want 2", len(all))
	}
	byEnv, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: "env-1"})
	if err != nil {
		t.Fatalf("ListQueueItems(env): %v", err)
	}
	if len(byEnv) != 2 {
		t.Fatalf("byEnv len = %d, want 2", len(byEnv))
	}
	byDom, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{RecipientDomain: "dest.test"})
	if err != nil {
		t.Fatalf("ListQueueItems(domain): %v", err)
	}
	if len(byDom) != 2 {
		t.Fatalf("byDom len = %d, want 2", len(byDom))
	}
	byOther, err := s.Meta().ListQueueItems(ctx, store.QueueFilter{RecipientDomain: "absent.test"})
	if err != nil {
		t.Fatalf("ListQueueItems(domain absent): %v", err)
	}
	if len(byOther) != 0 {
		t.Fatalf("byOther len = %d, want 0", len(byOther))
	}
}

func testQueueIdempotency(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-idem@example.com")
	ref := putBlob(t, s, "idem-body")
	id1, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID:    p.ID,
		MailFrom:       "alice@example.com",
		RcptTo:         "x@dest.test",
		EnvelopeID:     "env-idem",
		BodyBlobHash:   ref.Hash,
		IdempotencyKey: "submit-7",
	})
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	id2, err := s.Meta().EnqueueMessage(ctx, store.QueueItem{
		PrincipalID:    p.ID,
		MailFrom:       "alice@example.com",
		RcptTo:         "x@dest.test",
		EnvelopeID:     "env-idem",
		BodyBlobHash:   ref.Hash,
		IdempotencyKey: "submit-7",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second enqueue err = %v, want ErrConflict", err)
	}
	if id2 != id1 {
		t.Fatalf("idempotent dedupe returned id=%d, want %d", id2, id1)
	}
}

func testQueueClaimDueTransitionsState(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-claim@example.com")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	id1 := mustEnqueue(t, s, store.QueueItem{
		PrincipalID:   p.ID,
		MailFrom:      "alice@example.com",
		RcptTo:        "due@dest.test",
		EnvelopeID:    "env-due",
		NextAttemptAt: now.Add(-time.Minute),
	})
	_ = mustEnqueue(t, s, store.QueueItem{
		PrincipalID:   p.ID,
		MailFrom:      "alice@example.com",
		RcptTo:        "future@dest.test",
		EnvelopeID:    "env-future",
		NextAttemptAt: now.Add(time.Hour),
	})
	due, err := s.Meta().ClaimDueQueueItems(ctx, now, 10)
	if err != nil {
		t.Fatalf("ClaimDueQueueItems: %v", err)
	}
	if len(due) != 1 || due[0].ID != id1 {
		t.Fatalf("claim returned %+v, want only id=%d", due, id1)
	}
	if due[0].State != store.QueueStateInflight {
		t.Fatalf("claimed row state = %v, want inflight", due[0].State)
	}
	got, err := s.Meta().GetQueueItem(ctx, id1)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if got.State != store.QueueStateInflight {
		t.Fatalf("row state after claim = %v", got.State)
	}
	if got.LastAttemptAt.IsZero() {
		t.Fatalf("LastAttemptAt was not stamped")
	}
}

func testQueueCompleteSuccessVsFailure(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-complete@example.com")
	idOK := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "ok@d.test", EnvelopeID: "ok",
	})
	idFail := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "fail@d.test", EnvelopeID: "fail",
	})
	if err := s.Meta().CompleteQueueItem(ctx, idOK, true, ""); err != nil {
		t.Fatalf("Complete OK: %v", err)
	}
	if err := s.Meta().CompleteQueueItem(ctx, idFail, false, "550 nope"); err != nil {
		t.Fatalf("Complete FAIL: %v", err)
	}
	gotOK, err := s.Meta().GetQueueItem(ctx, idOK)
	if err != nil {
		t.Fatalf("Get OK: %v", err)
	}
	if gotOK.State != store.QueueStateDone {
		t.Fatalf("ok state = %v, want done", gotOK.State)
	}
	gotFail, err := s.Meta().GetQueueItem(ctx, idFail)
	if err != nil {
		t.Fatalf("Get FAIL: %v", err)
	}
	if gotFail.State != store.QueueStateFailed {
		t.Fatalf("fail state = %v, want failed", gotFail.State)
	}
	if gotFail.LastError == "" {
		t.Fatalf("LastError empty")
	}
	if err := s.Meta().CompleteQueueItem(ctx, store.QueueItemID(99999), true, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Complete absent = %v, want ErrNotFound", err)
	}
}

func testQueueRescheduleBumpsAttempts(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-resch@example.com")
	id := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "x@d.test", EnvelopeID: "r",
	})
	next := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().RescheduleQueueItem(ctx, id, next, "451 try later"); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	got, err := s.Meta().GetQueueItem(ctx, id)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if got.State != store.QueueStateDeferred {
		t.Fatalf("state = %v, want deferred", got.State)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
	if !got.NextAttemptAt.Equal(next) {
		t.Fatalf("NextAttemptAt = %v, want %v", got.NextAttemptAt, next)
	}
	if got.LastError == "" {
		t.Fatalf("LastError empty after reschedule")
	}
	// Reschedule again should bump attempts.
	if err := s.Meta().RescheduleQueueItem(ctx, id, next.Add(time.Hour), "451 still later"); err != nil {
		t.Fatalf("Reschedule 2: %v", err)
	}
	got, err = s.Meta().GetQueueItem(ctx, id)
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts after 2nd reschedule = %d, want 2", got.Attempts)
	}
}

func testQueueHoldRelease(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-hold@example.com")
	id := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "h@d.test", EnvelopeID: "h",
	})
	if err := s.Meta().HoldQueueItem(ctx, id); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	got, _ := s.Meta().GetQueueItem(ctx, id)
	if got.State != store.QueueStateHeld {
		t.Fatalf("state = %v, want held", got.State)
	}
	if err := s.Meta().ReleaseQueueItem(ctx, id); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got, _ = s.Meta().GetQueueItem(ctx, id)
	if got.State != store.QueueStateQueued {
		t.Fatalf("state after release = %v, want queued", got.State)
	}
	if err := s.Meta().HoldQueueItem(ctx, store.QueueItemID(99999)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Hold absent = %v, want ErrNotFound", err)
	}
}

func testQueueDeleteCascadeOnPrincipalDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-cascade@example.com")
	id := mustEnqueue(t, s, store.QueueItem{
		PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "c@d.test", EnvelopeID: "c",
	})
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if _, err := s.Meta().GetQueueItem(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("queue row still present after principal delete: %v", err)
	}
}

func testQueueCountByState(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "queue-count@example.com")
	id1 := mustEnqueue(t, s, store.QueueItem{PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "1@d.test", EnvelopeID: "c1"})
	id2 := mustEnqueue(t, s, store.QueueItem{PrincipalID: p.ID, MailFrom: "a@example.com", RcptTo: "2@d.test", EnvelopeID: "c2"})
	if err := s.Meta().HoldQueueItem(ctx, id2); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	counts, err := s.Meta().CountQueueByState(ctx)
	if err != nil {
		t.Fatalf("CountQueueByState: %v", err)
	}
	if counts[store.QueueStateQueued] != 1 {
		t.Fatalf("queued count = %d, want 1", counts[store.QueueStateQueued])
	}
	if counts[store.QueueStateHeld] != 1 {
		t.Fatalf("held count = %d, want 1", counts[store.QueueStateHeld])
	}
	_ = id1
}

func testDKIMUpsertAndList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	keyA := store.DKIMKey{
		Domain: "ex.test", Selector: "sel-a", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "PRIV-A", PublicKeyB64: "PUB-A", Status: store.DKIMKeyStatusActive,
	}
	keyB := store.DKIMKey{
		Domain: "ex.test", Selector: "sel-b", Algorithm: store.DKIMAlgorithmEd25519SHA256,
		PrivateKeyPEM: "PRIV-B", PublicKeyB64: "PUB-B", Status: store.DKIMKeyStatusRetiring,
	}
	if err := s.Meta().UpsertDKIMKey(ctx, keyA); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := s.Meta().UpsertDKIMKey(ctx, keyB); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}
	// Idempotent re-upsert with status change.
	keyA.Status = store.DKIMKeyStatusRetiring
	if err := s.Meta().UpsertDKIMKey(ctx, keyA); err != nil {
		t.Fatalf("Upsert A (retire): %v", err)
	}
	list, err := s.Meta().ListDKIMKeys(ctx, "ex.test")
	if err != nil {
		t.Fatalf("ListDKIMKeys: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	for _, k := range list {
		if k.Status != store.DKIMKeyStatusRetiring {
			t.Fatalf("after upsert, %q status = %v", k.Selector, k.Status)
		}
	}
}

func testDKIMRotateOneTx(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if err := s.Meta().UpsertDKIMKey(ctx, store.DKIMKey{
		Domain: "rot.test", Selector: "old", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "OLD", PublicKeyB64: "OLDPUB", Status: store.DKIMKeyStatusActive,
	}); err != nil {
		t.Fatalf("Upsert old: %v", err)
	}
	if err := s.Meta().RotateDKIMKey(ctx, "rot.test", "old", store.DKIMKey{
		Domain: "rot.test", Selector: "new", Algorithm: store.DKIMAlgorithmEd25519SHA256,
		PrivateKeyPEM: "NEW", PublicKeyB64: "NEWPUB",
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	list, err := s.Meta().ListDKIMKeys(ctx, "rot.test")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var oldStatus, newStatus store.DKIMKeyStatus
	for _, k := range list {
		switch k.Selector {
		case "old":
			oldStatus = k.Status
		case "new":
			newStatus = k.Status
		}
	}
	if oldStatus != store.DKIMKeyStatusRetiring {
		t.Fatalf("old status = %v, want retiring", oldStatus)
	}
	if newStatus != store.DKIMKeyStatusActive {
		t.Fatalf("new status = %v, want active", newStatus)
	}
	// Rotating an absent selector returns NotFound.
	err = s.Meta().RotateDKIMKey(ctx, "rot.test", "ghost", store.DKIMKey{
		Domain: "rot.test", Selector: "n2", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "P", PublicKeyB64: "P",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Rotate ghost = %v, want ErrNotFound", err)
	}
}

func testDKIMActiveLookup(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().GetActiveDKIMKey(ctx, "absent.test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetActiveDKIMKey(absent) = %v", err)
	}
	if err := s.Meta().UpsertDKIMKey(ctx, store.DKIMKey{
		Domain: "act.test", Selector: "s1", Algorithm: store.DKIMAlgorithmRSASHA256,
		PrivateKeyPEM: "P", PublicKeyB64: "B", Status: store.DKIMKeyStatusActive,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Meta().GetActiveDKIMKey(ctx, "act.test")
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	if got.Selector != "s1" {
		t.Fatalf("selector = %q, want s1", got.Selector)
	}
}

func testACMEAccountOrderCertLifecycle(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	acc, err := s.Meta().UpsertACMEAccount(ctx, store.ACMEAccount{
		DirectoryURL:  "https://acme.test/dir",
		ContactEmail:  "ops@example.com",
		AccountKeyPEM: "ACCT-PEM",
	})
	if err != nil {
		t.Fatalf("UpsertACMEAccount: %v", err)
	}
	if acc.ID == 0 {
		t.Fatalf("account id unset")
	}
	// Idempotent re-upsert: same (directory_url, contact) returns same id.
	acc2, err := s.Meta().UpsertACMEAccount(ctx, store.ACMEAccount{
		DirectoryURL:  "https://acme.test/dir",
		ContactEmail:  "ops@example.com",
		AccountKeyPEM: "ACCT-PEM-V2",
		KID:           "kid-1",
	})
	if err != nil {
		t.Fatalf("UpsertACMEAccount v2: %v", err)
	}
	if acc2.ID != acc.ID {
		t.Fatalf("upsert id = %d, want %d", acc2.ID, acc.ID)
	}
	got, err := s.Meta().GetACMEAccount(ctx, "https://acme.test/dir", "ops@example.com")
	if err != nil {
		t.Fatalf("GetACMEAccount: %v", err)
	}
	if got.KID != "kid-1" {
		t.Fatalf("kid = %q after upsert", got.KID)
	}

	// Order.
	order, err := s.Meta().InsertACMEOrder(ctx, store.ACMEOrder{
		AccountID:     acc.ID,
		Hostnames:     []string{"a.example.com", "b.example.com"},
		Status:        store.ACMEOrderStatusPending,
		OrderURL:      "https://acme.test/order/1",
		FinalizeURL:   "https://acme.test/order/1/finalize",
		ChallengeType: store.ChallengeTypeHTTP01,
	})
	if err != nil {
		t.Fatalf("InsertACMEOrder: %v", err)
	}
	order.Status = store.ACMEOrderStatusValid
	order.CertificateURL = "https://acme.test/cert/1"
	if err := s.Meta().UpdateACMEOrder(ctx, order); err != nil {
		t.Fatalf("UpdateACMEOrder: %v", err)
	}
	gotOrder, err := s.Meta().GetACMEOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetACMEOrder: %v", err)
	}
	if gotOrder.Status != store.ACMEOrderStatusValid {
		t.Fatalf("status = %v", gotOrder.Status)
	}
	if len(gotOrder.Hostnames) != 2 {
		t.Fatalf("hostnames = %v", gotOrder.Hostnames)
	}
	byStatus, err := s.Meta().ListACMEOrdersByStatus(ctx, store.ACMEOrderStatusValid)
	if err != nil {
		t.Fatalf("ListACMEOrdersByStatus: %v", err)
	}
	if len(byStatus) != 1 {
		t.Fatalf("byStatus len = %d, want 1", len(byStatus))
	}

	// Cert.
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname:      "a.example.com",
		ChainPEM:      "CHAIN",
		PrivateKeyPEM: "KEY",
		NotBefore:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:      notAfter,
		Issuer:        "Test CA",
		OrderID:       order.ID,
	}); err != nil {
		t.Fatalf("UpsertACMECert: %v", err)
	}
	cert, err := s.Meta().GetACMECert(ctx, "a.example.com")
	if err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
	if !cert.NotAfter.Equal(notAfter) {
		t.Fatalf("NotAfter = %v", cert.NotAfter)
	}
}

func testACMECertExpiringList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	soon := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	far := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname: "soon.test", ChainPEM: "C", PrivateKeyPEM: "K",
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: soon,
	}); err != nil {
		t.Fatalf("Upsert soon: %v", err)
	}
	if err := s.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname: "far.test", ChainPEM: "C", PrivateKeyPEM: "K",
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: far,
	}); err != nil {
		t.Fatalf("Upsert far: %v", err)
	}
	expiring, err := s.Meta().ListACMECertsExpiringBefore(ctx, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ListACMECertsExpiringBefore: %v", err)
	}
	if len(expiring) != 1 || expiring[0].Hostname != "soon.test" {
		t.Fatalf("expiring = %+v", expiring)
	}
}

func testWebhookCRUD(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	w, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind:    store.WebhookOwnerDomain,
		OwnerID:      "example.com",
		TargetURL:    "https://hook.example.com/incoming",
		HMACSecret:   []byte("secret"),
		DeliveryMode: store.DeliveryModeInline,
		RetryPolicy:  store.RetryPolicy{MaxAttempts: 5, InitialBackoffMS: 1000},
		Active:       true,
	})
	if err != nil {
		t.Fatalf("InsertWebhook: %v", err)
	}
	if w.ID == 0 || w.CreatedAt.IsZero() {
		t.Fatalf("webhook = %+v", w)
	}
	got, err := s.Meta().GetWebhook(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWebhook: %v", err)
	}
	if got.RetryPolicy.MaxAttempts != 5 {
		t.Fatalf("RetryPolicy roundtrip = %+v", got.RetryPolicy)
	}
	got.Active = false
	got.TargetURL = "https://hook.example.com/v2"
	if err := s.Meta().UpdateWebhook(ctx, got); err != nil {
		t.Fatalf("UpdateWebhook: %v", err)
	}
	got2, err := s.Meta().GetWebhook(ctx, w.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got2.Active || got2.TargetURL != "https://hook.example.com/v2" {
		t.Fatalf("update did not persist: %+v", got2)
	}
	listed, err := s.Meta().ListWebhooks(ctx, store.WebhookOwnerDomain, "example.com")
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListWebhooks = %d, want 1", len(listed))
	}
	if err := s.Meta().DeleteWebhook(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if err := s.Meta().DeleteWebhook(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteWebhook absent = %v", err)
	}
}

func testWebhookActiveForDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "wh-user@example.com")
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "example.com",
		TargetURL: "https://x.test/d", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("Insert domain: %v", err)
	}
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerPrincipal, OwnerID: fmt.Sprintf("%d", p.ID),
		TargetURL: "https://x.test/p", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("Insert principal: %v", err)
	}
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "other.test",
		TargetURL: "https://x.test/o", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: true,
	}); err != nil {
		t.Fatalf("Insert other: %v", err)
	}
	if _, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind: store.WebhookOwnerDomain, OwnerID: "example.com",
		TargetURL: "https://x.test/inactive", HMACSecret: []byte("s"),
		DeliveryMode: store.DeliveryModeInline, Active: false,
	}); err != nil {
		t.Fatalf("Insert inactive: %v", err)
	}
	hooks, err := s.Meta().ListActiveWebhooksForDomain(ctx, "example.com")
	if err != nil {
		t.Fatalf("ListActiveWebhooksForDomain: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("active hooks = %d, want 2 (one domain, one principal): %+v", len(hooks), hooks)
	}
}

// testWebhookSyntheticAndExtractedRoundTrip exercises the Phase 3
// Wave 3.5c additions (REQ-HOOK-02 + REQ-HOOK-EXTRACTED-01..03):
// target_kind=synthetic surfaces in ListActiveWebhooksForDomain
// alongside legacy domain hooks, and the body-mode / cap / drop-flag
// columns round-trip across both backends.
func testWebhookSyntheticAndExtractedRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	w, err := s.Meta().InsertWebhook(ctx, store.Webhook{
		OwnerKind:             store.WebhookOwnerDomain,
		OwnerID:               "app.example.com",
		TargetKind:            store.WebhookTargetSynthetic,
		TargetURL:             "https://app.internal/v1/mail/inbound",
		HMACSecret:            []byte("synth-secret"),
		DeliveryMode:          store.DeliveryModeInline,
		BodyMode:              store.WebhookBodyModeExtracted,
		ExtractedTextMaxBytes: 5 * 1024 * 1024,
		TextRequired:          true,
		Active:                true,
	})
	if err != nil {
		t.Fatalf("InsertWebhook: %v", err)
	}
	got, err := s.Meta().GetWebhook(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWebhook: %v", err)
	}
	if got.TargetKind != store.WebhookTargetSynthetic {
		t.Fatalf("TargetKind round-trip: %v", got.TargetKind)
	}
	if got.BodyMode != store.WebhookBodyModeExtracted {
		t.Fatalf("BodyMode round-trip: %v", got.BodyMode)
	}
	if got.ExtractedTextMaxBytes != 5*1024*1024 {
		t.Fatalf("ExtractedTextMaxBytes round-trip: %d", got.ExtractedTextMaxBytes)
	}
	if !got.TextRequired {
		t.Fatalf("TextRequired round-trip: %v", got.TextRequired)
	}
	hooks, err := s.Meta().ListActiveWebhooksForDomain(ctx, "app.example.com")
	if err != nil {
		t.Fatalf("ListActiveWebhooksForDomain: %v", err)
	}
	var found bool
	for _, h := range hooks {
		if h.ID == w.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("synthetic hook not surfaced by ListActiveWebhooksForDomain: %+v", hooks)
	}
}

func testDMARCInsertAndAggregate(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	begin := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	report := store.DMARCReport{
		ReceivedAt:    time.Date(2026, 5, 2, 1, 0, 0, 0, time.UTC),
		ReporterEmail: "noreply@reporter.test",
		ReporterOrg:   "Reporter Org",
		ReportID:      "rpt-001",
		Domain:        "ex.test",
		DateBegin:     begin,
		DateEnd:       end,
		XMLBlobHash:   strings.Repeat("a", 64),
		ParsedOK:      true,
	}
	rows := []store.DMARCRow{
		{SourceIP: "1.1.1.1", Count: 10, Disposition: 0, SPFAligned: true, DKIMAligned: true, HeaderFrom: "ex.test"},
		{SourceIP: "2.2.2.2", Count: 5, Disposition: 1, SPFAligned: false, DKIMAligned: false, HeaderFrom: "ex.test"},
	}
	id, err := s.Meta().InsertDMARCReport(ctx, report, rows)
	if err != nil {
		t.Fatalf("InsertDMARCReport: %v", err)
	}
	if id == 0 {
		t.Fatalf("id = 0")
	}
	gotRep, gotRows, err := s.Meta().GetDMARCReport(ctx, id)
	if err != nil {
		t.Fatalf("GetDMARCReport: %v", err)
	}
	if gotRep.ReporterOrg != "Reporter Org" {
		t.Fatalf("reporter = %q", gotRep.ReporterOrg)
	}
	if len(gotRows) != 2 {
		t.Fatalf("rows = %d, want 2", len(gotRows))
	}
	listed, err := s.Meta().ListDMARCReports(ctx, store.DMARCReportFilter{Domain: "ex.test"})
	if err != nil {
		t.Fatalf("ListDMARCReports: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %d, want 1", len(listed))
	}
	agg, err := s.Meta().DMARCAggregate(ctx, "ex.test", begin, end.Add(time.Hour))
	if err != nil {
		t.Fatalf("DMARCAggregate: %v", err)
	}
	if len(agg) != 2 {
		t.Fatalf("agg = %d, want 2", len(agg))
	}
	var totalCount int64
	for _, a := range agg {
		totalCount += a.Count
	}
	if totalCount != 15 {
		t.Fatalf("total count = %d, want 15", totalCount)
	}
}

func testDMARCDeduplicates(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	report := store.DMARCReport{
		ReceivedAt:    time.Now().UTC(),
		ReporterEmail: "n@r.test", ReporterOrg: "Org", ReportID: "dup-1",
		Domain: "d.test", DateBegin: time.Now().UTC(), DateEnd: time.Now().UTC(),
		XMLBlobHash: "hash", ParsedOK: true,
	}
	if _, err := s.Meta().InsertDMARCReport(ctx, report, nil); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.Meta().InsertDMARCReport(ctx, report, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("dup insert = %v, want ErrConflict", err)
	}
}

func testMailboxACLGrantListRevoke(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "acl-owner@example.com")
	other := mustInsertPrincipal(t, s, "acl-other@example.com")
	mb := mustInsertMailbox(t, s, owner.ID, "Shared")
	pid := other.ID
	if err := s.Meta().SetMailboxACL(ctx, mb.ID, &pid,
		store.ACLRightLookup|store.ACLRightRead, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL: %v", err)
	}
	got, err := s.Meta().GetMailboxACL(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("acl rows = %d, want 1", len(got))
	}
	if !got[0].Rights.CanRead() {
		t.Fatalf("Rights.CanRead = false, want true")
	}
	accessible, err := s.Meta().ListMailboxesAccessibleBy(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListMailboxesAccessibleBy: %v", err)
	}
	if len(accessible) != 1 || accessible[0].ID != mb.ID {
		t.Fatalf("accessible = %+v", accessible)
	}
	// Update rights on existing row.
	if err := s.Meta().SetMailboxACL(ctx, mb.ID, &pid,
		store.ACLRightLookup|store.ACLRightRead|store.ACLRightWrite, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL update: %v", err)
	}
	got, _ = s.Meta().GetMailboxACL(ctx, mb.ID)
	if len(got) != 1 {
		t.Fatalf("after update rows = %d", len(got))
	}
	if !got[0].Rights.CanWrite() {
		t.Fatalf("CanWrite false after update")
	}
	if err := s.Meta().RemoveMailboxACL(ctx, mb.ID, &pid); err != nil {
		t.Fatalf("RemoveMailboxACL: %v", err)
	}
	if err := s.Meta().RemoveMailboxACL(ctx, mb.ID, &pid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RemoveMailboxACL absent = %v", err)
	}
	accessible, _ = s.Meta().ListMailboxesAccessibleBy(ctx, other.ID)
	if len(accessible) != 0 {
		t.Fatalf("accessible after revoke = %+v", accessible)
	}
}

func testMailboxACLAnyoneRow(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "acl-anyone-owner@example.com")
	other := mustInsertPrincipal(t, s, "acl-anyone-other@example.com")
	mb := mustInsertMailbox(t, s, owner.ID, "Public")
	if err := s.Meta().SetMailboxACL(ctx, mb.ID, nil,
		store.ACLRightLookup|store.ACLRightRead, owner.ID); err != nil {
		t.Fatalf("SetMailboxACL anyone: %v", err)
	}
	rows, err := s.Meta().GetMailboxACL(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	if len(rows) != 1 || rows[0].PrincipalID != nil {
		t.Fatalf("anyone row = %+v", rows)
	}
	// Anyone row grants other principals access.
	accessible, err := s.Meta().ListMailboxesAccessibleBy(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListMailboxesAccessibleBy: %v", err)
	}
	if len(accessible) != 1 {
		t.Fatalf("anyone access = %+v", accessible)
	}
	// Remove anyone row.
	if err := s.Meta().RemoveMailboxACL(ctx, mb.ID, nil); err != nil {
		t.Fatalf("Remove anyone: %v", err)
	}
}

func testJMAPStatesIncrementAtomic(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "jmap@example.com")
	st, err := s.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	if st.Mailbox != 0 || st.Email != 0 {
		t.Fatalf("initial state = %+v", st)
	}
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindEmail); err != nil {
				t.Errorf("IncrementJMAPState: %v", err)
			}
		}()
	}
	wg.Wait()
	st, err = s.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates after increments: %v", err)
	}
	if st.Email != n {
		t.Fatalf("Email state = %d, want %d", st.Email, n)
	}
	// Independent kinds must not interfere.
	for i := 0; i < 3; i++ {
		if _, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindMailbox); err != nil {
			t.Fatalf("Increment Mailbox: %v", err)
		}
	}
	st, _ = s.Meta().GetJMAPStates(ctx, p.ID)
	if st.Mailbox != 3 {
		t.Fatalf("Mailbox state = %d, want 3", st.Mailbox)
	}
	if st.Email != n {
		t.Fatalf("Email state = %d, want %d (mailbox bumps must not affect email)", st.Email, n)
	}
}

func testTLSRPTAppendAndRange(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := s.Meta().AppendTLSRPTFailure(ctx, store.TLSRPTFailure{
			RecordedAt:           base.Add(time.Duration(i) * time.Hour),
			PolicyDomain:         "ex.test",
			ReceivingMTAHostname: "mx.peer.test",
			FailureType:          store.TLSRPTFailureMTASTS,
			FailureCode:          fmt.Sprintf("F%d", i),
			FailureDetailJSON:    `{"detail":"x"}`,
		}); err != nil {
			t.Fatalf("AppendTLSRPTFailure %d: %v", i, err)
		}
	}
	if err := s.Meta().AppendTLSRPTFailure(ctx, store.TLSRPTFailure{
		RecordedAt: base, PolicyDomain: "other.test", FailureType: store.TLSRPTFailureDANE,
	}); err != nil {
		t.Fatalf("AppendTLSRPTFailure other: %v", err)
	}
	rng, err := s.Meta().ListTLSRPTFailures(ctx, "ex.test", base.Add(time.Hour), base.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("ListTLSRPTFailures: %v", err)
	}
	if len(rng) != 3 {
		t.Fatalf("range len = %d, want 3", len(rng))
	}
	for i := 1; i < len(rng); i++ {
		if rng[i].RecordedAt.Before(rng[i-1].RecordedAt) {
			t.Fatalf("not ordered: %v then %v", rng[i-1].RecordedAt, rng[i].RecordedAt)
		}
	}
	all, err := s.Meta().ListTLSRPTFailures(ctx, "other.test", base, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListTLSRPTFailures other: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("other domain len = %d, want 1", len(all))
	}
}

// -- Wave 2.2.5 JMAP persistence ------------------------------------

func testEmailSubmissionInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esa@example.com")
	row := store.EmailSubmissionRow{
		ID:          "env-001",
		EnvelopeID:  store.EnvelopeID("env-001"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     store.MessageID(42),
		ThreadID:    "T1",
		SendAtUs:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		CreatedAtUs: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		UndoStatus:  "pending",
		Properties:  []byte(`{"x":1}`),
	}
	if err := s.Meta().InsertEmailSubmission(ctx, row); err != nil {
		t.Fatalf("InsertEmailSubmission: %v", err)
	}
	got, err := s.Meta().GetEmailSubmission(ctx, "env-001")
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if got.ID != row.ID || got.EnvelopeID != row.EnvelopeID || got.PrincipalID != row.PrincipalID {
		t.Fatalf("identity mismatch: got %+v want %+v", got, row)
	}
	if got.IdentityID != "default" || got.EmailID != 42 || got.ThreadID != "T1" {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if got.UndoStatus != "pending" {
		t.Fatalf("undoStatus = %q, want pending", got.UndoStatus)
	}
	if string(got.Properties) != `{"x":1}` {
		t.Fatalf("properties = %q, want {\"x\":1}", got.Properties)
	}
	// Duplicate insert.
	if err := s.Meta().InsertEmailSubmission(ctx, row); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate Insert: err = %v, want ErrConflict", err)
	}
	// Missing.
	if _, err := s.Meta().GetEmailSubmission(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func testEmailSubmissionListFilters(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esl@example.com")
	q := mustInsertPrincipal(t, s, "other@example.com")
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, identity, thread string, eid store.MessageID, send time.Time, undo string, owner store.PrincipalID) {
		err := s.Meta().InsertEmailSubmission(ctx, store.EmailSubmissionRow{
			ID:          id,
			EnvelopeID:  store.EnvelopeID(id),
			PrincipalID: owner,
			IdentityID:  identity,
			EmailID:     eid,
			ThreadID:    thread,
			SendAtUs:    send.UnixMicro(),
			CreatedAtUs: send.UnixMicro(),
			UndoStatus:  undo,
		})
		if err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	mk("a", "default", "T1", 1, base, "pending", p.ID)
	mk("b", "alt", "T1", 2, base.Add(time.Hour), "final", p.ID)
	mk("c", "default", "T2", 3, base.Add(2*time.Hour), "pending", p.ID)
	mk("d", "default", "T1", 4, base.Add(3*time.Hour), "canceled", p.ID)
	mk("e", "default", "T1", 5, base.Add(4*time.Hour), "pending", q.ID)
	// Principal isolation: e is q's, must never appear in p's list.
	all, err := s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{})
	if err != nil {
		t.Fatalf("ListEmailSubmissions: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("all len = %d, want 4", len(all))
	}
	// Default sort is SendAtUs ASC.
	for i := 1; i < len(all); i++ {
		if all[i].SendAtUs < all[i-1].SendAtUs {
			t.Fatalf("not ascending: %v then %v", all[i-1].SendAtUs, all[i].SendAtUs)
		}
	}
	// IdentityIDs.
	got, err := s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{IdentityIDs: []string{"alt"}})
	if err != nil {
		t.Fatalf("List(IdentityIDs): %v", err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("IdentityIDs filter: %+v", got)
	}
	// EmailIDs.
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{EmailIDs: []store.MessageID{1, 4}})
	if err != nil {
		t.Fatalf("List(EmailIDs): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("EmailIDs filter len = %d, want 2", len(got))
	}
	// ThreadIDs.
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{ThreadIDs: []string{"T2"}})
	if err != nil {
		t.Fatalf("List(ThreadIDs): %v", err)
	}
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("ThreadIDs filter: %+v", got)
	}
	// UndoStatus.
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{UndoStatus: "pending"})
	if err != nil {
		t.Fatalf("List(UndoStatus): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("UndoStatus pending len = %d, want 2", len(got))
	}
	// Time range: After base.Add(30m) and Before base.Add(2h30m).
	got, err = s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{
		AfterUs:  base.Add(30 * time.Minute).UnixMicro(),
		BeforeUs: base.Add(2*time.Hour + 30*time.Minute).UnixMicro(),
	})
	if err != nil {
		t.Fatalf("List(time range): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("time range len = %d, want 2", len(got))
	}
}

func testEmailSubmissionUpdateUndoStatus(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esuus@example.com")
	row := store.EmailSubmissionRow{
		ID:          "env-u1",
		EnvelopeID:  store.EnvelopeID("env-u1"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     1,
		SendAtUs:    time.Now().UnixMicro(),
		CreatedAtUs: time.Now().UnixMicro(),
		UndoStatus:  "pending",
	}
	if err := s.Meta().InsertEmailSubmission(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().UpdateEmailSubmissionUndoStatus(ctx, "env-u1", "canceled"); err != nil {
		t.Fatalf("UpdateEmailSubmissionUndoStatus: %v", err)
	}
	got, err := s.Meta().GetEmailSubmission(ctx, "env-u1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UndoStatus != "canceled" {
		t.Fatalf("undoStatus = %q, want canceled", got.UndoStatus)
	}
	// Missing row → ErrNotFound.
	if err := s.Meta().UpdateEmailSubmissionUndoStatus(ctx, "nope", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing: err = %v, want ErrNotFound", err)
	}
}

func testEmailSubmissionDeleteNotFoundAfter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esdel@example.com")
	row := store.EmailSubmissionRow{
		ID:          "env-d1",
		EnvelopeID:  store.EnvelopeID("env-d1"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     1,
		SendAtUs:    time.Now().UnixMicro(),
		CreatedAtUs: time.Now().UnixMicro(),
	}
	if err := s.Meta().InsertEmailSubmission(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteEmailSubmission(ctx, "env-d1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta().GetEmailSubmission(ctx, "env-d1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	// Second delete returns ErrNotFound.
	if err := s.Meta().DeleteEmailSubmission(ctx, "env-d1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete twice: err = %v, want ErrNotFound", err)
	}
}

func testEmailSubmissionExternalFlagRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "esext@example.com")
	// Insert an external submission row (External = true).
	extRow := store.EmailSubmissionRow{
		ID:          "ext-env-001",
		EnvelopeID:  store.EnvelopeID("ext-env-001"),
		PrincipalID: p.ID,
		IdentityID:  "alt",
		EmailID:     store.MessageID(99),
		ThreadID:    "Tx",
		SendAtUs:    time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		CreatedAtUs: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMicro(),
		UndoStatus:  "final",
		External:    true,
	}
	if err := s.Meta().InsertEmailSubmission(ctx, extRow); err != nil {
		t.Fatalf("InsertEmailSubmission (external=true): %v", err)
	}
	got, err := s.Meta().GetEmailSubmission(ctx, "ext-env-001")
	if err != nil {
		t.Fatalf("GetEmailSubmission: %v", err)
	}
	if !got.External {
		t.Fatalf("External flag: got false, want true")
	}
	// Insert an internal submission row (External = false) and confirm.
	intRow := store.EmailSubmissionRow{
		ID:          "int-env-001",
		EnvelopeID:  store.EnvelopeID("int-env-001"),
		PrincipalID: p.ID,
		IdentityID:  "default",
		EmailID:     store.MessageID(100),
		SendAtUs:    time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).UnixMicro(),
		CreatedAtUs: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC).UnixMicro(),
		UndoStatus:  "pending",
		External:    false,
	}
	if err := s.Meta().InsertEmailSubmission(ctx, intRow); err != nil {
		t.Fatalf("InsertEmailSubmission (external=false): %v", err)
	}
	got2, err := s.Meta().GetEmailSubmission(ctx, "int-env-001")
	if err != nil {
		t.Fatalf("GetEmailSubmission (internal): %v", err)
	}
	if got2.External {
		t.Fatalf("External flag: got true, want false for internal row")
	}
	// Both rows appear in list.
	list, err := s.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListEmailSubmissions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListEmailSubmissions: got %d rows, want 2", len(list))
	}
	extFound, intFound := false, false
	for _, r := range list {
		if r.ID == "ext-env-001" {
			extFound = true
			if !r.External {
				t.Fatalf("list: External flag false for ext-env-001")
			}
		}
		if r.ID == "int-env-001" {
			intFound = true
			if r.External {
				t.Fatalf("list: External flag true for int-env-001")
			}
		}
	}
	if !extFound || !intFound {
		t.Fatalf("list: extFound=%v intFound=%v", extFound, intFound)
	}
}

func testJMAPIdentityInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-rt@example.com")
	row := store.JMAPIdentity{
		ID:            "1",
		PrincipalID:   p.ID,
		Name:          "Display",
		Email:         "ji-rt@example.com",
		ReplyToJSON:   []byte(`[{"email":"reply@example.com"}]`),
		BccJSON:       []byte(`[{"email":"bcc@example.com"}]`),
		TextSignature: "sig",
		HTMLSignature: "<p>sig</p>",
		MayDelete:     true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, "1")
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if got.PrincipalID != p.ID || got.Name != "Display" || got.Email != "ji-rt@example.com" {
		t.Fatalf("scalar mismatch: %+v", got)
	}
	if string(got.ReplyToJSON) != `[{"email":"reply@example.com"}]` {
		t.Fatalf("ReplyToJSON = %q", got.ReplyToJSON)
	}
	if string(got.BccJSON) != `[{"email":"bcc@example.com"}]` {
		t.Fatalf("BccJSON = %q", got.BccJSON)
	}
	if got.TextSignature != "sig" || got.HTMLSignature != "<p>sig</p>" {
		t.Fatalf("signatures: %+v", got)
	}
	if !got.MayDelete {
		t.Fatalf("MayDelete = false, want true")
	}
	if got.CreatedAtUs == 0 || got.UpdatedAtUs == 0 {
		t.Fatalf("timestamps not populated: %+v", got)
	}
	// Conflict.
	if err := s.Meta().InsertJMAPIdentity(ctx, row); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate Insert: err = %v, want ErrConflict", err)
	}
	if _, err := s.Meta().GetJMAPIdentity(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func testJMAPIdentityListByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-l@example.com")
	q := mustInsertPrincipal(t, s, "ji-l-other@example.com")
	for _, row := range []store.JMAPIdentity{
		{ID: "10", PrincipalID: p.ID, Email: "a@example.com", MayDelete: true},
		{ID: "11", PrincipalID: p.ID, Email: "b@example.com", MayDelete: true},
		{ID: "20", PrincipalID: q.ID, Email: "c@example.com", MayDelete: true},
	} {
		if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
			t.Fatalf("Insert %s: %v", row.ID, err)
		}
	}
	got, err := s.Meta().ListJMAPIdentities(ctx, p.ID)
	if err != nil {
		t.Fatalf("List p: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("p list len = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.PrincipalID != p.ID {
			t.Fatalf("foreign principal leaked: %+v", r)
		}
	}
	got, err = s.Meta().ListJMAPIdentities(ctx, q.ID)
	if err != nil {
		t.Fatalf("List q: %v", err)
	}
	if len(got) != 1 || got[0].ID != "20" {
		t.Fatalf("q list = %+v", got)
	}
}

func testJMAPIdentityUpdateRoundtrips(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-up@example.com")
	row := store.JMAPIdentity{
		ID: "100", PrincipalID: p.ID, Email: "ji-up@example.com",
		Name: "Old", TextSignature: "old", MayDelete: true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	row.Name = "New"
	row.TextSignature = "new"
	row.HTMLSignature = "<b>new</b>"
	row.ReplyToJSON = []byte(`[{"email":"r@x"}]`)
	row.BccJSON = []byte(`[{"email":"b@x"}]`)
	if err := s.Meta().UpdateJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("UpdateJMAPIdentity: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, "100")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "New" || got.TextSignature != "new" || got.HTMLSignature != "<b>new</b>" {
		t.Fatalf("update did not stick: %+v", got)
	}
	if string(got.ReplyToJSON) != `[{"email":"r@x"}]` || string(got.BccJSON) != `[{"email":"b@x"}]` {
		t.Fatalf("json fields: %+v", got)
	}
	// Update of missing row.
	row.ID = "missing"
	if err := s.Meta().UpdateJMAPIdentity(ctx, row); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing: err = %v, want ErrNotFound", err)
	}
}

func testJMAPIdentityDeleteNotFoundAfter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-del@example.com")
	row := store.JMAPIdentity{
		ID: "200", PrincipalID: p.ID, Email: "ji-del@example.com", MayDelete: true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteJMAPIdentity(ctx, "200"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta().GetJMAPIdentity(ctx, "200"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	if err := s.Meta().DeleteJMAPIdentity(ctx, "200"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete twice: err = %v, want ErrNotFound", err)
	}
}

// -- REQ-PROTO-49 JMAP snooze ----------------------------------------

// snoozeKeywordPresent reports whether m.Keywords contains "$snoozed".
func snoozeKeywordPresent(m store.Message) bool {
	for _, k := range m.Keywords {
		if k == "$snoozed" {
			return true
		}
	}
	return false
}

func testSnoozeSetGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "snooze-rt@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "snooze-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	t1 := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := s.Meta().SetSnooze(ctx, id, mb.ID, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	got, err := s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.SnoozedUntil == nil || !got.SnoozedUntil.Equal(t1) {
		t.Fatalf("SnoozedUntil = %v, want %v", got.SnoozedUntil, t1)
	}
	if !snoozeKeywordPresent(got) {
		t.Fatalf("Keywords = %v, want $snoozed present", got.Keywords)
	}
}

func testSnoozeClear(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "snooze-clear@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "snooze-clear-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	t1 := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := s.Meta().SetSnooze(ctx, id, mb.ID, &t1); err != nil {
		t.Fatalf("SetSnooze set: %v", err)
	}
	if _, err := s.Meta().SetSnooze(ctx, id, mb.ID, nil); err != nil {
		t.Fatalf("SetSnooze clear: %v", err)
	}
	got, err := s.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.SnoozedUntil != nil {
		t.Fatalf("SnoozedUntil = %v, want nil", got.SnoozedUntil)
	}
	if snoozeKeywordPresent(got) {
		t.Fatalf("Keywords = %v, $snoozed should be cleared", got.Keywords)
	}
}

func testSnoozeStateChangeAppended(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "snooze-sc@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "snooze-sc-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	// Capture the current change-feed cursor (post-create).
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed pre: %v", err)
	}
	var cursor store.ChangeSeq
	for _, e := range feed {
		if e.Seq > cursor {
			cursor = e.Seq
		}
	}
	t1 := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := s.Meta().SetSnooze(ctx, id, mb.ID, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	tail, err := s.Meta().ReadChangeFeed(ctx, p.ID, cursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed tail: %v", err)
	}
	if len(tail) == 0 {
		t.Fatalf("expected state change appended, got 0")
	}
	last := tail[len(tail)-1]
	if last.Kind != store.EntityKindEmail {
		t.Fatalf("Kind = %v, want %v", last.Kind, store.EntityKindEmail)
	}
	if last.Op != store.ChangeOpUpdated {
		t.Fatalf("Op = %v, want %v", last.Op, store.ChangeOpUpdated)
	}
	if last.EntityID != uint64(id) {
		t.Fatalf("EntityID = %d, want %d", last.EntityID, id)
	}
}

func testSnoozeListDueOnlyReturnsDue(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "snooze-due@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ids := make([]store.MessageID, 0, 3)
	deadlines := []time.Time{
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC),
	}
	for i := range deadlines {
		ref := putBlob(t, s, fmt.Sprintf("body-%d", i))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			ids = append(ids, store.MessageID(e.EntityID))
		}
	}
	if len(ids) != 3 {
		t.Fatalf("created %d messages, want 3", len(ids))
	}
	for i, id := range ids {
		if _, err := s.Meta().SetSnooze(ctx, id, mb.ID, &deadlines[i]); err != nil {
			t.Fatalf("SetSnooze[%d]: %v", i, err)
		}
	}
	// now == t2 → t1 and t2 due, t3 not.
	got, err := s.Meta().ListDueSnoozedMessages(ctx, deadlines[1], 100)
	if err != nil {
		t.Fatalf("ListDueSnoozedMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d due, want 2: %v", len(got), got)
	}
	for _, m := range got {
		if m.SnoozedUntil == nil || m.SnoozedUntil.After(deadlines[1]) {
			t.Fatalf("returned non-due message: %v", m)
		}
	}
}

func testSnoozeListDueLimit(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "snooze-lim@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	due := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		ref := putBlob(t, s, fmt.Sprintf("body-%d", i))
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			if _, err := s.Meta().SetSnooze(ctx, store.MessageID(e.EntityID), mb.ID, &due); err != nil {
				t.Fatalf("SetSnooze: %v", err)
			}
		}
	}
	now := due.Add(time.Hour)
	got, err := s.Meta().ListDueSnoozedMessages(ctx, now, 3)
	if err != nil {
		t.Fatalf("ListDueSnoozedMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (limit)", len(got))
	}
}

func testSnoozeListDueRequiresKeyword(t *testing.T, s store.Store) {
	// Programmer-error case: a row whose snoozed_until column is set
	// but whose keywords do NOT contain "$snoozed" must be invisible
	// to ListDueSnoozedMessages. This proves the AND condition is
	// real. We cannot reach this state via SetSnooze (which sets
	// both), so we use UpdateMessageFlags to remove the keyword
	// after SetSnooze planted both — and assert the row drops out of
	// the due list.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "snooze-kw@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	ref := putBlob(t, s, "snooze-kw-body")
	if _, _, err := s.Meta().InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	id := firstMessageIDFromFeed(t, s, p.ID)
	due := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Meta().SetSnooze(ctx, id, mb.ID, &due); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	// Sanity: due list returns the row right now.
	got, err := s.Meta().ListDueSnoozedMessages(ctx, due.Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("ListDue (sanity): %v", err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("sanity: got %v, want exactly id=%d", got, id)
	}
	// Now remove the keyword via UpdateMessageFlags directly. The
	// SnoozedUntil column remains set (programmer-error shape).
	if _, err := s.Meta().UpdateMessageFlags(ctx, id, mb.ID, 0, 0, nil, []string{"$snoozed"}, 0); err != nil {
		t.Fatalf("UpdateMessageFlags clear: %v", err)
	}
	got2, err := s.Meta().ListDueSnoozedMessages(ctx, due.Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("ListDue post-clear: %v", err)
	}
	if len(got2) != 0 {
		t.Fatalf("ListDue after keyword-only clear: got %d, want 0", len(got2))
	}
}

// -- REQ-FILT-200..221 LLM categorisation ----------------------------

func testCategorisationConfigDefaults(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-defaults@example.com")
	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	if cfg.PrincipalID != p.ID {
		t.Fatalf("PrincipalID = %d, want %d", cfg.PrincipalID, p.ID)
	}
	if !cfg.Enabled {
		t.Fatalf("default Enabled = false, want true")
	}
	if cfg.Prompt == "" {
		t.Fatalf("default Prompt is empty; expected seeded text")
	}
	if len(cfg.CategorySet) == 0 {
		t.Fatalf("default CategorySet is empty; expected seeded categories")
	}
	// Default categories must include the documented Gmail-style set.
	wanted := map[string]bool{
		"primary": false, "social": false, "promotions": false,
		"updates": false, "forums": false,
	}
	for _, c := range cfg.CategorySet {
		if _, ok := wanted[c.Name]; ok {
			wanted[c.Name] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("default seed missing category %q", name)
		}
	}
	if cfg.TimeoutSec <= 0 {
		t.Fatalf("default TimeoutSec = %d, want > 0", cfg.TimeoutSec)
	}
	// A second read returns the same row; the seed is stable.
	cfg2, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (second): %v", err)
	}
	if cfg2.Prompt != cfg.Prompt {
		t.Fatalf("second read Prompt diverged: %q vs %q", cfg2.Prompt, cfg.Prompt)
	}
	if len(cfg2.CategorySet) != len(cfg.CategorySet) {
		t.Fatalf("second read CategorySet length = %d, want %d", len(cfg2.CategorySet), len(cfg.CategorySet))
	}
}

func testCategorisationConfigRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-rt@example.com")
	endpoint := "https://example.test/v1"
	model := "custom-model"
	keyEnv := "MY_API_KEY"
	cfg := store.CategorisationConfig{
		PrincipalID: p.ID,
		Prompt:      "you are a custom classifier",
		CategorySet: []store.CategoryDef{
			{Name: "work", Description: "Work emails."},
			{Name: "newsletters", Description: "Newsletters and digests."},
		},
		Endpoint:   &endpoint,
		Model:      &model,
		APIKeyEnv:  &keyEnv,
		TimeoutSec: 12,
		Enabled:    false,
	}
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}
	got, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Prompt != cfg.Prompt {
		t.Fatalf("Prompt = %q, want %q", got.Prompt, cfg.Prompt)
	}
	if len(got.CategorySet) != 2 || got.CategorySet[0].Name != "work" || got.CategorySet[1].Name != "newsletters" {
		t.Fatalf("CategorySet round-trip = %+v, want 2 entries [work, newsletters]", got.CategorySet)
	}
	if got.CategorySet[0].Description != "Work emails." {
		t.Fatalf("Description round-trip = %q, want %q", got.CategorySet[0].Description, "Work emails.")
	}
	if got.Endpoint == nil || *got.Endpoint != endpoint {
		t.Fatalf("Endpoint round-trip = %v, want %q", got.Endpoint, endpoint)
	}
	if got.Model == nil || *got.Model != model {
		t.Fatalf("Model round-trip = %v, want %q", got.Model, model)
	}
	if got.APIKeyEnv == nil || *got.APIKeyEnv != keyEnv {
		t.Fatalf("APIKeyEnv round-trip = %v, want %q", got.APIKeyEnv, keyEnv)
	}
	if got.TimeoutSec != 12 {
		t.Fatalf("TimeoutSec = %d, want 12", got.TimeoutSec)
	}
	if got.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
	// Update again clearing the optional overrides.
	cfg2 := got
	cfg2.Endpoint = nil
	cfg2.Model = nil
	cfg2.APIKeyEnv = nil
	cfg2.Enabled = true
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg2); err != nil {
		t.Fatalf("UpdateCategorisationConfig (clear): %v", err)
	}
	got2, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get (post-clear): %v", err)
	}
	if got2.Endpoint != nil || got2.Model != nil || got2.APIKeyEnv != nil {
		t.Fatalf("optional overrides did not clear: %+v", got2)
	}
	if !got2.Enabled {
		t.Fatalf("Enabled = false, want true after re-enable")
	}
}

func testCategorisationConfigGuardrailRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-guardrail@example.com")
	cfg := store.CategorisationConfig{
		PrincipalID: p.ID,
		Prompt:      "user-visible prompt text",
		Guardrail:   "operator-only guardrail text",
		CategorySet: []store.CategoryDef{{Name: "primary", Description: "Primary."}},
		TimeoutSec:  5,
		Enabled:     true,
	}
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}
	got, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	if got.Prompt != cfg.Prompt {
		t.Fatalf("Prompt = %q, want %q", got.Prompt, cfg.Prompt)
	}
	if got.Guardrail != cfg.Guardrail {
		t.Fatalf("Guardrail = %q, want %q", got.Guardrail, cfg.Guardrail)
	}
	// Update with empty guardrail; should clear.
	cfg.Guardrail = ""
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig (clear guardrail): %v", err)
	}
	got2, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (post-clear): %v", err)
	}
	if got2.Guardrail != "" {
		t.Fatalf("Guardrail after clear = %q, want empty", got2.Guardrail)
	}
}

func testCategorisationConfigDerivedCategoriesRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-derived@example.com")
	// Seed the config row and read the epoch.
	seed, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	want := []string{"primary", "social", "promotions", "updates", "forums"}
	ok, err := s.Meta().SetDerivedCategories(ctx, p.ID, want, seed.DerivedCategoriesEpoch)
	if err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}
	if !ok {
		t.Fatalf("SetDerivedCategories: expected hit, got miss")
	}
	got, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig after SetDerivedCategories: %v", err)
	}
	if len(got.DerivedCategories) != len(want) {
		t.Fatalf("DerivedCategories = %v, want %v", got.DerivedCategories, want)
	}
	for i, name := range want {
		if got.DerivedCategories[i] != name {
			t.Errorf("DerivedCategories[%d] = %q, want %q", i, got.DerivedCategories[i], name)
		}
	}
}

func testCategorisationConfigPromptChangeClearsDerived(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-prompt-clear@example.com")
	// Seed and set derived categories.
	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	if _, err := s.Meta().SetDerivedCategories(ctx, p.ID, []string{"primary", "social"}, cfg.DerivedCategoriesEpoch); err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}
	// Verify they are set.
	cfg2, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (after set): %v", err)
	}
	if len(cfg2.DerivedCategories) == 0 {
		t.Fatalf("expected DerivedCategories to be set before prompt change")
	}
	// Change the prompt via UpdateCategorisationConfig.
	cfg.Prompt = "completely different prompt text"
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig (prompt change): %v", err)
	}
	got, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (after prompt change): %v", err)
	}
	// DerivedCategories must be cleared after the prompt changed (REQ-FILT-217).
	if len(got.DerivedCategories) != 0 {
		t.Fatalf("DerivedCategories not cleared after prompt change: %v", got.DerivedCategories)
	}
	if got.Prompt != cfg.Prompt {
		t.Fatalf("Prompt not updated: %q, want %q", got.Prompt, cfg.Prompt)
	}
}

// testCategorisationConfigEpochGuardStaleWriteDropped verifies that a
// SetDerivedCategories call carrying a stale epoch (i.e., the caller read
// the config before a prompt-change that bumped the epoch) is silently
// dropped: the method returns false and the NULL written by the prompt-change
// is preserved.
func testCategorisationConfigEpochGuardStaleWriteDropped(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-epoch-stale@example.com")

	// Seed and read the initial epoch.
	cfg0, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	epoch0 := cfg0.DerivedCategoriesEpoch

	// Simulate: classifier starts, reads epoch 0.
	// Meanwhile: prompt changes via UpdateCategorisationConfig — bumps epoch.
	cfg0.Prompt = "new prompt that changes the epoch"
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg0); err != nil {
		t.Fatalf("UpdateCategorisationConfig (prompt change): %v", err)
	}

	// Verify epoch was bumped.
	cfg1, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (post-prompt-change): %v", err)
	}
	if cfg1.DerivedCategoriesEpoch <= epoch0 {
		t.Fatalf("epoch not bumped after prompt change: got %d, want > %d",
			cfg1.DerivedCategoriesEpoch, epoch0)
	}
	if len(cfg1.DerivedCategories) != 0 {
		t.Fatalf("DerivedCategories not cleared after prompt change: %v", cfg1.DerivedCategories)
	}

	// Classifier finishes, tries to write with the OLD epoch — must be dropped.
	ok, err := s.Meta().SetDerivedCategories(ctx, p.ID, []string{"primary", "social"}, epoch0)
	if err != nil {
		t.Fatalf("SetDerivedCategories (stale epoch): %v", err)
	}
	if ok {
		t.Fatalf("SetDerivedCategories returned true with stale epoch — want false")
	}

	// The NULL must still be persisted.
	got, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (final): %v", err)
	}
	if len(got.DerivedCategories) != 0 {
		t.Fatalf("stale write persisted DerivedCategories = %v, want nil", got.DerivedCategories)
	}
}

// testCategorisationConfigEpochBumpsOnPromptChange verifies that the epoch
// increments on every prompt change and stays unchanged when only non-prompt
// fields are updated.
func testCategorisationConfigEpochBumpsOnPromptChange(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-epoch-bump@example.com")

	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	epoch0 := cfg.DerivedCategoriesEpoch

	// Write with same prompt — epoch must not change.
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig (same prompt): %v", err)
	}
	cfg1, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (same prompt): %v", err)
	}
	if cfg1.DerivedCategoriesEpoch != epoch0 {
		t.Fatalf("epoch changed on same-prompt update: got %d, want %d",
			cfg1.DerivedCategoriesEpoch, epoch0)
	}

	// Write with changed prompt — epoch must increment.
	cfg1.Prompt = "a new prompt"
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg1); err != nil {
		t.Fatalf("UpdateCategorisationConfig (new prompt): %v", err)
	}
	cfg2, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (new prompt): %v", err)
	}
	if cfg2.DerivedCategoriesEpoch != epoch0+1 {
		t.Fatalf("epoch after prompt change: got %d, want %d",
			cfg2.DerivedCategoriesEpoch, epoch0+1)
	}

	// Write with changed prompt again — epoch must increment once more.
	cfg2.Prompt = "yet another prompt"
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg2); err != nil {
		t.Fatalf("UpdateCategorisationConfig (second new prompt): %v", err)
	}
	cfg3, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (second new prompt): %v", err)
	}
	if cfg3.DerivedCategoriesEpoch != epoch0+2 {
		t.Fatalf("epoch after second prompt change: got %d, want %d",
			cfg3.DerivedCategoriesEpoch, epoch0+2)
	}
}

// -- REQ-FILT-66 / REQ-FILT-216 / G14 LLM classification records -----

func testLLMClassificationSpamOnly(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "llm-spam@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "llm-spam@host")

	verdict := "ham"
	confidence := 0.95
	reason := "looks good"
	prompt := "spam system prompt"
	model := "llama3:8b"
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	rec := store.LLMClassificationRecord{
		MessageID:         msg.ID,
		PrincipalID:       p.ID,
		SpamVerdict:       &verdict,
		SpamConfidence:    &confidence,
		SpamReason:        &reason,
		SpamPromptApplied: &prompt,
		SpamModel:         &model,
		SpamClassifiedAt:  &now,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}
	got, err := s.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if got.MessageID != msg.ID {
		t.Fatalf("MessageID = %d, want %d", got.MessageID, msg.ID)
	}
	if got.SpamVerdict == nil || *got.SpamVerdict != verdict {
		t.Fatalf("SpamVerdict = %v, want %q", got.SpamVerdict, verdict)
	}
	if got.SpamConfidence == nil || *got.SpamConfidence != confidence {
		t.Fatalf("SpamConfidence = %v, want %v", got.SpamConfidence, confidence)
	}
	if got.SpamReason == nil || *got.SpamReason != reason {
		t.Fatalf("SpamReason = %v, want %q", got.SpamReason, reason)
	}
	if got.SpamPromptApplied == nil || *got.SpamPromptApplied != prompt {
		t.Fatalf("SpamPromptApplied = %v, want %q", got.SpamPromptApplied, prompt)
	}
	if got.SpamModel == nil || *got.SpamModel != model {
		t.Fatalf("SpamModel = %v, want %q", got.SpamModel, model)
	}
	if got.SpamClassifiedAt == nil || !got.SpamClassifiedAt.Equal(now) {
		t.Fatalf("SpamClassifiedAt = %v, want %v", got.SpamClassifiedAt, now)
	}
	// Category fields must be nil.
	if got.CategoryAssigned != nil {
		t.Fatalf("CategoryAssigned should be nil, got %q", *got.CategoryAssigned)
	}
}

func testLLMClassificationCategoryOnly(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "llm-cat@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "llm-cat@host")

	cat := "promotions"
	catPrompt := "categorise this email"
	catModel := "gpt-4o-mini"
	catAt := time.Date(2024, 3, 1, 8, 0, 0, 0, time.UTC)

	rec := store.LLMClassificationRecord{
		MessageID:             msg.ID,
		PrincipalID:           p.ID,
		CategoryAssigned:      &cat,
		CategoryPromptApplied: &catPrompt,
		CategoryModel:         &catModel,
		CategoryClassifiedAt:  &catAt,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}
	got, err := s.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if got.CategoryAssigned == nil || *got.CategoryAssigned != cat {
		t.Fatalf("CategoryAssigned = %v, want %q", got.CategoryAssigned, cat)
	}
	if got.CategoryModel == nil || *got.CategoryModel != catModel {
		t.Fatalf("CategoryModel = %v, want %q", got.CategoryModel, catModel)
	}
	if got.SpamVerdict != nil {
		t.Fatalf("SpamVerdict should be nil for category-only record, got %q", *got.SpamVerdict)
	}
}

func testLLMClassificationBothFields(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "llm-both@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "llm-both@host")

	verdict := "spam"
	conf := 0.87
	cat := "promotions"

	rec := store.LLMClassificationRecord{
		MessageID:        msg.ID,
		PrincipalID:      p.ID,
		SpamVerdict:      &verdict,
		SpamConfidence:   &conf,
		CategoryAssigned: &cat,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}
	got, err := s.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if got.SpamVerdict == nil || *got.SpamVerdict != verdict {
		t.Fatalf("SpamVerdict = %v, want %q", got.SpamVerdict, verdict)
	}
	if got.CategoryAssigned == nil || *got.CategoryAssigned != cat {
		t.Fatalf("CategoryAssigned = %v, want %q", got.CategoryAssigned, cat)
	}
}

func testLLMClassificationUpsertSpamThenCategory(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "llm-upsert@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "llm-upsert@host")

	verdict := "ham"
	rec1 := store.LLMClassificationRecord{
		MessageID:   msg.ID,
		PrincipalID: p.ID,
		SpamVerdict: &verdict,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec1); err != nil {
		t.Fatalf("SetLLMClassification (spam): %v", err)
	}

	cat := "social"
	rec2 := store.LLMClassificationRecord{
		MessageID:        msg.ID,
		PrincipalID:      p.ID,
		CategoryAssigned: &cat,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec2); err != nil {
		t.Fatalf("SetLLMClassification (category): %v", err)
	}

	got, err := s.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	// Both spam and category must survive the two-step upsert.
	if got.SpamVerdict == nil || *got.SpamVerdict != verdict {
		t.Fatalf("SpamVerdict after upsert = %v, want %q", got.SpamVerdict, verdict)
	}
	if got.CategoryAssigned == nil || *got.CategoryAssigned != cat {
		t.Fatalf("CategoryAssigned after upsert = %v, want %q", got.CategoryAssigned, cat)
	}
}

func testLLMClassificationBatchGet(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "llm-batch@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg1 := mustInsertMessage(t, s, mb.ID, "llm-batch-1@host")
	msg2 := mustInsertMessage(t, s, mb.ID, "llm-batch-2@host")
	msg3 := mustInsertMessage(t, s, mb.ID, "llm-batch-3@host")

	v1, v2 := "ham", "spam"
	if err := s.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID: msg1.ID, PrincipalID: p.ID, SpamVerdict: &v1,
	}); err != nil {
		t.Fatalf("SetLLMClassification msg1: %v", err)
	}
	if err := s.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID: msg2.ID, PrincipalID: p.ID, SpamVerdict: &v2,
	}); err != nil {
		t.Fatalf("SetLLMClassification msg2: %v", err)
	}
	// msg3 has no classification.

	batch, err := s.Meta().BatchGetLLMClassifications(ctx, []store.MessageID{msg1.ID, msg2.ID, msg3.ID})
	if err != nil {
		t.Fatalf("BatchGetLLMClassifications: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch len = %d, want 2 (msg3 has no record)", len(batch))
	}
	r1, ok := batch[msg1.ID]
	if !ok {
		t.Fatal("msg1 missing from batch")
	}
	if r1.SpamVerdict == nil || *r1.SpamVerdict != v1 {
		t.Fatalf("msg1 SpamVerdict = %v, want %q", r1.SpamVerdict, v1)
	}
	r2, ok := batch[msg2.ID]
	if !ok {
		t.Fatal("msg2 missing from batch")
	}
	if r2.SpamVerdict == nil || *r2.SpamVerdict != v2 {
		t.Fatalf("msg2 SpamVerdict = %v, want %q", r2.SpamVerdict, v2)
	}
}

func testLLMClassificationGetNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	_, err := s.Meta().GetLLMClassification(ctx, store.MessageID(999999))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetLLMClassification for absent id: err = %v, want ErrNotFound", err)
	}
}

// -- Wave 2.5 (REQ-PROTO-53/56/57; REQ-STORE-34/35) ----------------

func testMailboxColorRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mb-color@example.com")
	colour := "#5B8DEE"
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Coloured", Color: &colour,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	got, err := s.Meta().GetMailboxByID(ctx, mb.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID: %v", err)
	}
	if got.Color == nil || *got.Color != colour {
		t.Fatalf("Color after insert = %v, want %q", got.Color, colour)
	}
	other := "#abcdef"
	if err := s.Meta().SetMailboxColor(ctx, mb.ID, &other); err != nil {
		t.Fatalf("SetMailboxColor(set): %v", err)
	}
	got, _ = s.Meta().GetMailboxByID(ctx, mb.ID)
	if got.Color == nil || *got.Color != other {
		t.Fatalf("Color after Set = %v, want %q", got.Color, other)
	}
	if err := s.Meta().SetMailboxColor(ctx, mb.ID, nil); err != nil {
		t.Fatalf("SetMailboxColor(clear): %v", err)
	}
	got, _ = s.Meta().GetMailboxByID(ctx, mb.ID)
	if got.Color != nil {
		t.Fatalf("Color after clear = %v, want nil", *got.Color)
	}
	plain, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Plain",
	})
	if err != nil {
		t.Fatalf("InsertMailbox plain: %v", err)
	}
	gotPlain, _ := s.Meta().GetMailboxByID(ctx, plain.ID)
	if gotPlain.Color != nil {
		t.Fatalf("plain mailbox Color = %v, want nil", *gotPlain.Color)
	}
	bad := "#000000"
	if err := s.Meta().SetMailboxColor(ctx, store.MailboxID(99999), &bad); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetMailboxColor on missing = %v, want ErrNotFound", err)
	}
}

func testMailboxColorRejectsInvalid(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "mb-color-bad@example.com")
	mb, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Plain",
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	for _, bad := range []string{"red", "#abc", "#GGGGGG", "#1234567", "5B8DEE", ""} {
		v := bad
		if err := s.Meta().SetMailboxColor(ctx, mb.ID, &v); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("SetMailboxColor(%q) = %v, want ErrInvalidArgument", bad, err)
		}
	}
	badc := "not-hex"
	if _, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Bad", Color: &badc,
	}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertMailbox(bad colour) = %v, want ErrInvalidArgument", err)
	}
}

func testJMAPIdentitySignatureRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ji-sig@example.com")
	sig := "Cheers,\nAlice"
	row := store.JMAPIdentity{
		ID:          "500",
		PrincipalID: p.ID,
		Email:       "ji-sig@example.com",
		MayDelete:   true,
		Signature:   &sig,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, "500")
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if got.Signature == nil || *got.Signature != sig {
		t.Fatalf("Signature = %v, want %q", got.Signature, sig)
	}
	other := "Best,\nA."
	row.Signature = &other
	if err := s.Meta().UpdateJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("UpdateJMAPIdentity: %v", err)
	}
	got, _ = s.Meta().GetJMAPIdentity(ctx, "500")
	if got.Signature == nil || *got.Signature != other {
		t.Fatalf("Signature after update = %v, want %q", got.Signature, other)
	}
	row.Signature = nil
	if err := s.Meta().UpdateJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("UpdateJMAPIdentity(clear): %v", err)
	}
	got, _ = s.Meta().GetJMAPIdentity(ctx, "500")
	if got.Signature != nil {
		t.Fatalf("Signature after clear = %v, want nil", *got.Signature)
	}
}

func testJMAPStatesSieveCounter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sieve-state@example.com")
	st, err := s.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	if st.Sieve != 0 {
		t.Fatalf("initial Sieve = %d, want 0", st.Sieve)
	}
	for i := 0; i < 3; i++ {
		v, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindSieve)
		if err != nil {
			t.Fatalf("Increment Sieve: %v", err)
		}
		if v != int64(i+1) {
			t.Fatalf("Sieve return = %d, want %d", v, i+1)
		}
	}
	st, _ = s.Meta().GetJMAPStates(ctx, p.ID)
	if st.Sieve != 3 {
		t.Fatalf("Sieve = %d, want 3", st.Sieve)
	}
	if st.Mailbox != 0 || st.Email != 0 || st.Identity != 0 {
		t.Fatalf("sibling counters drifted: %+v", st)
	}
}

// -- Wave 2.7 JMAP for Calendars (REQ-PROTO-54) ---------------------

// testCalendarInsertGetRoundtrip exercises InsertCalendar / GetCalendar
// preserving every field, the assigned ID, monotonic ModSeq, and the
// nullable Color pointer.
func testCalendarInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cal-rt@example.com")
	color := "#5B8DEE"
	id, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID:  p.ID,
		Name:         "Personal",
		Description:  "primary calendar",
		Color:        &color,
		SortOrder:    7,
		IsSubscribed: true,
		IsDefault:    true,
		IsVisible:    true,
		TimeZoneID:   "Europe/Berlin",
		RightsMask:   store.ACLRights(0xff),
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	got, err := s.Meta().GetCalendar(ctx, id)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got.ID != id || got.PrincipalID != p.ID || got.Name != "Personal" ||
		got.Description != "primary calendar" || got.SortOrder != 7 ||
		!got.IsSubscribed || !got.IsDefault || !got.IsVisible ||
		got.TimeZoneID != "Europe/Berlin" || got.RightsMask != store.ACLRights(0xff) {
		t.Fatalf("Calendar mismatch: %+v", got)
	}
	if got.Color == nil || *got.Color != color {
		t.Fatalf("Color = %v, want %q", got.Color, color)
	}
	if got.ModSeq != 1 {
		t.Fatalf("initial ModSeq = %d, want 1", got.ModSeq)
	}
}

// testCalendarListFilterAndPagination exercises principal filter,
// keyset pagination via AfterID, and the AfterModSeq cursor.
func testCalendarListFilterAndPagination(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "cal-page-1@example.com")
	p2 := mustInsertPrincipal(t, s, "cal-page-2@example.com")
	const n = 5
	var ids []store.CalendarID
	for i := 0; i < n; i++ {
		id, err := s.Meta().InsertCalendar(ctx, store.Calendar{
			PrincipalID: p1.ID,
			Name:        fmt.Sprintf("c-%02d", i),
			IsVisible:   true,
		})
		if err != nil {
			t.Fatalf("InsertCalendar: %v", err)
		}
		ids = append(ids, id)
	}
	// Other principal's calendar must not leak in.
	if _, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p2.ID, Name: "other",
	}); err != nil {
		t.Fatalf("InsertCalendar p2: %v", err)
	}

	page, err := s.Meta().ListCalendars(ctx, store.CalendarFilter{
		PrincipalID: &p1.ID, Limit: 3,
	})
	if err != nil {
		t.Fatalf("ListCalendars page1: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("page1 len = %d, want 3", len(page))
	}
	for _, c := range page {
		if c.PrincipalID != p1.ID {
			t.Fatalf("leaked principal: %+v", c)
		}
	}
	// Continue from the last ID.
	rest, err := s.Meta().ListCalendars(ctx, store.CalendarFilter{
		PrincipalID: &p1.ID, AfterID: page[2].ID,
	})
	if err != nil {
		t.Fatalf("ListCalendars page2: %v", err)
	}
	if len(rest) != n-3 {
		t.Fatalf("page2 len = %d, want %d", len(rest), n-3)
	}

	// AfterModSeq: bumping one row's modseq must surface only that row.
	one, err := s.Meta().GetCalendar(ctx, ids[2])
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	one.Description = "touched"
	if err := s.Meta().UpdateCalendar(ctx, one); err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	delta, err := s.Meta().ListCalendars(ctx, store.CalendarFilter{
		PrincipalID: &p1.ID, AfterModSeq: 1,
	})
	if err != nil {
		t.Fatalf("ListCalendars AfterModSeq: %v", err)
	}
	if len(delta) != 1 || delta[0].ID != ids[2] {
		t.Fatalf("delta = %+v, want only id %d", delta, ids[2])
	}
}

// testCalendarUpdateBumpsModSeq verifies UpdateCalendar increments
// ModSeq, persists mutated fields, and emits a state-change row.
func testCalendarUpdateBumpsModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cal-update@example.com")
	id, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "Work", IsVisible: true,
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	c, _ := s.Meta().GetCalendar(ctx, id)
	priorSeq := c.ModSeq
	c.Name = "Work (renamed)"
	c.SortOrder = 99
	if err := s.Meta().UpdateCalendar(ctx, c); err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	got, _ := s.Meta().GetCalendar(ctx, id)
	if got.ModSeq <= priorSeq {
		t.Fatalf("ModSeq did not advance: got %d, prior %d", got.ModSeq, priorSeq)
	}
	if got.Name != "Work (renamed)" || got.SortOrder != 99 {
		t.Fatalf("Update did not persist: %+v", got)
	}
	// State-change feed: at least a (calendar, created) followed by
	// (calendar, updated).
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var sawCreated, sawUpdated bool
	for _, e := range feed {
		if e.Kind == store.EntityKindCalendar && uint64(id) == e.EntityID {
			switch e.Op {
			case store.ChangeOpCreated:
				sawCreated = true
			case store.ChangeOpUpdated:
				sawUpdated = true
			}
		}
	}
	if !sawCreated || !sawUpdated {
		t.Fatalf("feed missing calendar create/update: %+v", feed)
	}
}

// testCalendarDefaultEnforcement verifies the auto-flip strategy: at
// most one default calendar per principal at any time. Marking a new
// default flips the previous default off in the same tx.
func testCalendarDefaultEnforcement(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cal-default@example.com")
	a, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "First", IsDefault: true, IsVisible: true,
	})
	if err != nil {
		t.Fatalf("InsertCalendar a: %v", err)
	}
	// Inserting another with IsDefault=true must auto-flip a's default.
	b, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "Second", IsDefault: true, IsVisible: true,
	})
	if err != nil {
		t.Fatalf("InsertCalendar b: %v", err)
	}
	got, err := s.Meta().DefaultCalendar(ctx, p.ID)
	if err != nil {
		t.Fatalf("DefaultCalendar: %v", err)
	}
	if got.ID != b {
		t.Fatalf("DefaultCalendar = %d, want %d", got.ID, b)
	}
	prev, _ := s.Meta().GetCalendar(ctx, a)
	if prev.IsDefault {
		t.Fatalf("prior default not flipped off: %+v", prev)
	}
	// Promoting a back via Update must again flip b off.
	prev.IsDefault = true
	if err := s.Meta().UpdateCalendar(ctx, prev); err != nil {
		t.Fatalf("UpdateCalendar promote: %v", err)
	}
	got2, _ := s.Meta().DefaultCalendar(ctx, p.ID)
	if got2.ID != a {
		t.Fatalf("DefaultCalendar after re-promote = %d, want %d", got2.ID, a)
	}
	bAfter, _ := s.Meta().GetCalendar(ctx, b)
	if bAfter.IsDefault {
		t.Fatalf("second default not flipped: %+v", bAfter)
	}
}

// testCalendarDeleteCascadesEvents inserts events into a calendar,
// deletes the calendar, and verifies events vanish + the feed carries
// per-event destroyed rows plus the calendar destroyed row.
func testCalendarDeleteCascadesEvents(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cal-cascade@example.com")
	cal, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "ToDestroy",
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	const n = 3
	var evIDs []store.CalendarEventID
	for i := 0; i < n; i++ {
		eid, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
			CalendarID:     cal,
			PrincipalID:    p.ID,
			UID:            fmt.Sprintf("uid-%d", i),
			JSCalendarJSON: []byte(`{"@type":"Event"}`),
			Start:          time.Unix(int64(i*3600), 0).UTC(),
			End:            time.Unix(int64(i*3600+1800), 0).UTC(),
			Summary:        fmt.Sprintf("event %d", i),
			Status:         "confirmed",
		})
		if err != nil {
			t.Fatalf("InsertCalendarEvent %d: %v", i, err)
		}
		evIDs = append(evIDs, eid)
	}
	if err := s.Meta().DeleteCalendar(ctx, cal); err != nil {
		t.Fatalf("DeleteCalendar: %v", err)
	}
	for _, eid := range evIDs {
		if _, err := s.Meta().GetCalendarEvent(ctx, eid); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetCalendarEvent(%d) = %v, want ErrNotFound", eid, err)
		}
	}
	if _, err := s.Meta().GetCalendar(ctx, cal); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCalendar after delete = %v, want ErrNotFound", err)
	}
	feed, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	destroyedEvents := 0
	var sawCalendarDestroyed bool
	for _, e := range feed {
		switch e.Kind {
		case store.EntityKindCalendarEvent:
			if e.Op == store.ChangeOpDestroyed && e.ParentEntityID == uint64(cal) {
				destroyedEvents++
			}
		case store.EntityKindCalendar:
			if e.Op == store.ChangeOpDestroyed && e.EntityID == uint64(cal) {
				sawCalendarDestroyed = true
			}
		}
	}
	if destroyedEvents != n {
		t.Fatalf("destroyed event rows = %d, want %d", destroyedEvents, n)
	}
	if !sawCalendarDestroyed {
		t.Fatalf("missing calendar destroyed row in feed")
	}
}

// testCalendarEventInsertGetRoundtrip preserves every field including
// the JSCalendar JSON blob and denormalised columns.
func testCalendarEventInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-rt@example.com")
	cal, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "ev-rt",
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	js := []byte(`{"@type":"Event","title":"Standup"}`)
	rrule := []byte(`{"@type":"RecurrenceRule","frequency":"weekly"}`)
	start := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	id, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
		CalendarID:     cal,
		PrincipalID:    p.ID,
		UID:            "rt-uid",
		JSCalendarJSON: js,
		Start:          start,
		End:            end,
		IsRecurring:    true,
		RRuleJSON:      rrule,
		Summary:        "Standup",
		OrganizerEmail: "Alice@Example.Com",
		Status:         "confirmed",
	})
	if err != nil {
		t.Fatalf("InsertCalendarEvent: %v", err)
	}
	got, err := s.Meta().GetCalendarEvent(ctx, id)
	if err != nil {
		t.Fatalf("GetCalendarEvent: %v", err)
	}
	if got.UID != "rt-uid" || got.Summary != "Standup" ||
		got.OrganizerEmail != "alice@example.com" || got.Status != "confirmed" ||
		!got.IsRecurring {
		t.Fatalf("event mismatch: %+v", got)
	}
	if !got.Start.Equal(start) || !got.End.Equal(end) {
		t.Fatalf("times mismatch: start=%v end=%v", got.Start, got.End)
	}
	if !bytes.Equal(got.JSCalendarJSON, js) {
		t.Fatalf("JSCalendarJSON: got %q want %q", got.JSCalendarJSON, js)
	}
	if !bytes.Equal(got.RRuleJSON, rrule) {
		t.Fatalf("RRuleJSON: got %q want %q", got.RRuleJSON, rrule)
	}
	if got.ModSeq != 1 {
		t.Fatalf("initial ModSeq = %d, want 1", got.ModSeq)
	}
}

// testCalendarEventListFilterByStartWindow inserts events at known
// instants and exercises the StartAfter / StartBefore window predicate.
func testCalendarEventListFilterByStartWindow(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-window@example.com")
	cal, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "win",
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
			CalendarID:     cal,
			PrincipalID:    p.ID,
			UID:            fmt.Sprintf("w-%d", i),
			JSCalendarJSON: []byte("{}"),
			Start:          base.Add(time.Duration(i) * time.Hour),
			End:            base.Add(time.Duration(i)*time.Hour + 30*time.Minute),
			Summary:        fmt.Sprintf("e%d", i),
			Status:         "confirmed",
		})
		if err != nil {
			t.Fatalf("InsertCalendarEvent %d: %v", i, err)
		}
	}
	after := base.Add(1 * time.Hour)
	before := base.Add(4 * time.Hour)
	got, err := s.Meta().ListCalendarEvents(ctx, store.CalendarEventFilter{
		CalendarID:  &cal,
		StartAfter:  &after,
		StartBefore: &before,
	})
	if err != nil {
		t.Fatalf("ListCalendarEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("window len = %d, want 3 (e1,e2,e3)", len(got))
	}
	for _, e := range got {
		if e.Start.Before(after) || !e.Start.Before(before) {
			t.Fatalf("event %s outside window: start=%v", e.UID, e.Start)
		}
	}
}

// testCalendarEventListFilterByUID checks the UID predicate selects
// exactly one row.
func testCalendarEventListFilterByUID(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-uid@example.com")
	cal, _ := s.Meta().InsertCalendar(ctx, store.Calendar{PrincipalID: p.ID, Name: "u"})
	for i := 0; i < 3; i++ {
		if _, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
			CalendarID: cal, PrincipalID: p.ID,
			UID:            fmt.Sprintf("uid-%d", i),
			JSCalendarJSON: []byte("{}"),
			Start:          time.Unix(int64(i), 0).UTC(),
			End:            time.Unix(int64(i+1), 0).UTC(),
		}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	target := "uid-1"
	got, err := s.Meta().ListCalendarEvents(ctx, store.CalendarEventFilter{
		CalendarID: &cal, UID: &target,
	})
	if err != nil {
		t.Fatalf("ListCalendarEvents: %v", err)
	}
	if len(got) != 1 || got[0].UID != target {
		t.Fatalf("uid filter = %+v, want one row uid=%q", got, target)
	}
}

// testCalendarEventListFilterByText exercises the case-insensitive
// substring filter on Summary.
func testCalendarEventListFilterByText(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-text@example.com")
	cal, _ := s.Meta().InsertCalendar(ctx, store.Calendar{PrincipalID: p.ID, Name: "t"})
	titles := []string{"Daily Standup", "Quarterly Review", "Lunch", "Standup retro"}
	for i, title := range titles {
		if _, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
			CalendarID: cal, PrincipalID: p.ID,
			UID:            fmt.Sprintf("t-%d", i),
			JSCalendarJSON: []byte("{}"),
			Start:          time.Unix(int64(i), 0).UTC(),
			End:            time.Unix(int64(i+1), 0).UTC(),
			Summary:        title,
		}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := s.Meta().ListCalendarEvents(ctx, store.CalendarEventFilter{
		CalendarID: &cal, Text: "stand",
	})
	if err != nil {
		t.Fatalf("ListCalendarEvents text: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("text filter len = %d, want 2 (Daily Standup, Standup retro)", len(got))
	}
}

// testCalendarEventUpdateBumpsModSeq verifies UpdateCalendarEvent
// advances ModSeq, persists denormalised columns, and feeds an updated
// state-change row.
func testCalendarEventUpdateBumpsModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-update@example.com")
	cal, _ := s.Meta().InsertCalendar(ctx, store.Calendar{PrincipalID: p.ID, Name: "u"})
	id, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
		CalendarID: cal, PrincipalID: p.ID,
		UID:            "u1",
		JSCalendarJSON: []byte(`{"v":1}`),
		Start:          time.Unix(100, 0).UTC(),
		End:            time.Unix(200, 0).UTC(),
		Summary:        "old",
		Status:         "confirmed",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	cur, _ := s.Meta().GetCalendarEvent(ctx, id)
	prior := cur.ModSeq
	cur.Summary = "new"
	cur.JSCalendarJSON = []byte(`{"v":2}`)
	cur.Status = "tentative"
	if err := s.Meta().UpdateCalendarEvent(ctx, cur); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Meta().GetCalendarEvent(ctx, id)
	if got.ModSeq <= prior {
		t.Fatalf("ModSeq stuck: %d -> %d", prior, got.ModSeq)
	}
	if got.Summary != "new" || got.Status != "tentative" {
		t.Fatalf("update not persisted: %+v", got)
	}
	if !bytes.Equal(got.JSCalendarJSON, []byte(`{"v":2}`)) {
		t.Fatalf("json not persisted: %q", got.JSCalendarJSON)
	}
	feed, _ := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	var sawUpdated bool
	for _, e := range feed {
		if e.Kind == store.EntityKindCalendarEvent && e.Op == store.ChangeOpUpdated &&
			e.EntityID == uint64(id) && e.ParentEntityID == uint64(cal) {
			sawUpdated = true
		}
	}
	if !sawUpdated {
		t.Fatalf("missing event-updated row")
	}
}

// testCalendarEventDeleteAppendsStateChange verifies DeleteCalendarEvent
// removes the row, returns ErrNotFound on the second call, and emits
// the destroyed state-change row.
func testCalendarEventDeleteAppendsStateChange(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-del@example.com")
	cal, _ := s.Meta().InsertCalendar(ctx, store.Calendar{PrincipalID: p.ID, Name: "d"})
	id, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
		CalendarID: cal, PrincipalID: p.ID, UID: "del-uid",
		JSCalendarJSON: []byte("{}"),
		Start:          time.Unix(0, 0).UTC(), End: time.Unix(60, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeleteCalendarEvent(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta().GetCalendarEvent(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCalendarEvent after delete = %v", err)
	}
	if err := s.Meta().DeleteCalendarEvent(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete twice = %v, want ErrNotFound", err)
	}
	feed, _ := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	var sawDestroyed bool
	for _, e := range feed {
		if e.Kind == store.EntityKindCalendarEvent && e.Op == store.ChangeOpDestroyed &&
			e.EntityID == uint64(id) && e.ParentEntityID == uint64(cal) {
			sawDestroyed = true
		}
	}
	if !sawDestroyed {
		t.Fatalf("missing destroyed row in feed: %+v", feed)
	}
}

// testCalendarEventGetByUID covers the iMIP RSVP-path lookup: ErrNotFound
// when absent; happy path returns the matching row.
func testCalendarEventGetByUID(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ev-uid-lookup@example.com")
	cal, _ := s.Meta().InsertCalendar(ctx, store.Calendar{PrincipalID: p.ID, Name: "k"})
	if _, err := s.Meta().GetCalendarEventByUID(ctx, cal, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCalendarEventByUID(absent) = %v, want ErrNotFound", err)
	}
	id, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
		CalendarID: cal, PrincipalID: p.ID, UID: "lookup-uid",
		JSCalendarJSON: []byte("{}"),
		Start:          time.Unix(0, 0).UTC(), End: time.Unix(60, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Meta().GetCalendarEventByUID(ctx, cal, "lookup-uid")
	if err != nil {
		t.Fatalf("GetCalendarEventByUID: %v", err)
	}
	if got.ID != id {
		t.Fatalf("by-uid id = %d, want %d", got.ID, id)
	}
}

// testJMAPStatesCalendarCounters race-tests the calendar +
// calendar_event JMAP state counters.
func testJMAPStatesCalendarCounters(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cal-jmap@example.com")
	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindCalendar); err != nil {
				t.Errorf("Increment Calendar: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindCalendarEvent); err != nil {
				t.Errorf("Increment CalendarEvent: %v", err)
			}
		}()
	}
	wg.Wait()
	st, err := s.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	if st.Calendar != n {
		t.Fatalf("Calendar = %d, want %d", st.Calendar, n)
	}
	if st.CalendarEvent != n {
		t.Fatalf("CalendarEvent = %d, want %d", st.CalendarEvent, n)
	}
	// Sibling counters not touched.
	if st.Mailbox != 0 || st.Email != 0 || st.AddressBook != 0 || st.Contact != 0 {
		t.Fatalf("sibling counters drifted: %+v", st)
	}
}

// testDeletePrincipalCascadesCalendars confirms DeletePrincipal sweeps
// owned calendars + events via FK cascade.
func testDeletePrincipalCascadesCalendars(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cal-del-principal@example.com")
	cal, err := s.Meta().InsertCalendar(ctx, store.Calendar{
		PrincipalID: p.ID, Name: "Owned",
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	eid, err := s.Meta().InsertCalendarEvent(ctx, store.CalendarEvent{
		CalendarID: cal, PrincipalID: p.ID, UID: "owned",
		JSCalendarJSON: []byte("{}"),
		Start:          time.Unix(0, 0).UTC(), End: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("InsertCalendarEvent: %v", err)
	}
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if _, err := s.Meta().GetCalendar(ctx, cal); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCalendar after DeletePrincipal = %v", err)
	}
	if _, err := s.Meta().GetCalendarEvent(ctx, eid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetCalendarEvent after DeletePrincipal = %v", err)
	}
}

// -- Wave 2.8 chat subsystem (REQ-CHAT-*) ----------------------------

// testChatConversationInsertGetRoundtrip preserves every column on
// the conversation row and assigns a monotonic ModSeq.
func testChatConversationInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-conv-rt@example.com")
	id, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "Project Phoenix",
		Topic:                "Phase 2 ship blockers",
		CreatedByPrincipalID: p.ID,
		MessageCount:         0,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	got, err := s.Meta().GetChatConversation(ctx, id)
	if err != nil {
		t.Fatalf("GetChatConversation: %v", err)
	}
	if got.ID != id || got.Kind != store.ChatConversationKindSpace ||
		got.Name != "Project Phoenix" || got.Topic != "Phase 2 ship blockers" ||
		got.CreatedByPrincipalID != p.ID || got.MessageCount != 0 ||
		got.IsArchived || got.ModSeq != 1 {
		t.Fatalf("conversation mismatch: %+v", got)
	}
	if got.LastMessageAt != nil {
		t.Fatalf("LastMessageAt should be nil on freshly-created conversation: %v", got.LastMessageAt)
	}
}

// testChatConversationListFilterByKind exercises the Kind +
// principal-creator filters and verifies archived conversations are
// excluded by default.
func testChatConversationListFilterByKind(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-list-kind@example.com")
	dm, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindDM, CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation dm: %v", err)
	}
	sp, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "team",
		CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation space: %v", err)
	}
	dmKind := store.ChatConversationKindDM
	got, err := s.Meta().ListChatConversations(ctx, store.ChatConversationFilter{
		Kind: &dmKind, CreatedByPrincipalID: &p.ID,
	})
	if err != nil {
		t.Fatalf("ListChatConversations dm: %v", err)
	}
	if len(got) != 1 || got[0].ID != dm {
		t.Fatalf("dm filter = %+v, want one row id=%d", got, dm)
	}
	spKind := store.ChatConversationKindSpace
	got, err = s.Meta().ListChatConversations(ctx, store.ChatConversationFilter{Kind: &spKind})
	if err != nil {
		t.Fatalf("ListChatConversations space: %v", err)
	}
	if len(got) != 1 || got[0].ID != sp {
		t.Fatalf("space filter = %+v, want one row id=%d", got, sp)
	}
	// Archive the space and verify default-list excludes it.
	cur, _ := s.Meta().GetChatConversation(ctx, sp)
	cur.IsArchived = true
	if err := s.Meta().UpdateChatConversation(ctx, cur); err != nil {
		t.Fatalf("UpdateChatConversation archive: %v", err)
	}
	got, _ = s.Meta().ListChatConversations(ctx, store.ChatConversationFilter{Kind: &spKind})
	if len(got) != 0 {
		t.Fatalf("archived not excluded: %+v", got)
	}
	got, _ = s.Meta().ListChatConversations(ctx, store.ChatConversationFilter{
		Kind: &spKind, IncludeArchived: true,
	})
	if len(got) != 1 {
		t.Fatalf("IncludeArchived missed: %+v", got)
	}
}

// testChatConversationUpdateBumpsModSeq verifies UpdateChatConversation
// advances ModSeq, persists mutated fields, and emits an updated
// state-change row.
func testChatConversationUpdateBumpsModSeq(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-conv-upd@example.com")
	id, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "old",
		CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	cur, _ := s.Meta().GetChatConversation(ctx, id)
	prior := cur.ModSeq
	cur.Name = "new"
	cur.MessageCount = 5
	if err := s.Meta().UpdateChatConversation(ctx, cur); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Meta().GetChatConversation(ctx, id)
	if got.ModSeq <= prior {
		t.Fatalf("ModSeq stuck: %d -> %d", prior, got.ModSeq)
	}
	if got.Name != "new" || got.MessageCount != 5 {
		t.Fatalf("update not persisted: %+v", got)
	}
	feed, _ := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 100)
	var sawCreated, sawUpdated bool
	for _, e := range feed {
		if e.Kind == store.EntityKindConversation && uint64(id) == e.EntityID {
			switch e.Op {
			case store.ChangeOpCreated:
				sawCreated = true
			case store.ChangeOpUpdated:
				sawUpdated = true
			}
		}
	}
	if !sawCreated || !sawUpdated {
		t.Fatalf("feed missing conversation create/update: %+v", feed)
	}
}

// testChatConversationDeleteCascades verifies DeleteChatConversation
// drops the conversation, its memberships, and its messages, plus
// emits per-row destroyed state-change entries.
func testChatConversationDeleteCascades(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-conv-del@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "doomed",
		CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	memID, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: p.ID, Role: store.ChatRoleAdmin,
	})
	if err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}
	pidArg := p.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &pidArg,
		BodyText:          "hello",
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}
	if err := s.Meta().DeleteChatConversation(ctx, cid); err != nil {
		t.Fatalf("DeleteChatConversation: %v", err)
	}
	if _, err := s.Meta().GetChatConversation(ctx, cid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetChatConversation after delete = %v", err)
	}
	if _, err := s.Meta().GetChatMembership(ctx, cid, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetChatMembership after delete = %v", err)
	}
	if _, err := s.Meta().GetChatMessage(ctx, mid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetChatMessage after delete = %v", err)
	}
	feed, _ := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	var sawConvDestroyed, sawMsgDestroyed, sawMemDestroyed bool
	for _, e := range feed {
		if e.Op != store.ChangeOpDestroyed {
			continue
		}
		switch e.Kind {
		case store.EntityKindConversation:
			if e.EntityID == uint64(cid) {
				sawConvDestroyed = true
			}
		case store.EntityKindChatMessage:
			if e.EntityID == uint64(mid) && e.ParentEntityID == uint64(cid) {
				sawMsgDestroyed = true
			}
		case store.EntityKindMembership:
			if e.EntityID == uint64(memID) && e.ParentEntityID == uint64(cid) {
				sawMemDestroyed = true
			}
		}
	}
	if !sawConvDestroyed || !sawMsgDestroyed || !sawMemDestroyed {
		t.Fatalf("missing destroyed rows: conv=%v msg=%v mem=%v feed=%+v",
			sawConvDestroyed, sawMsgDestroyed, sawMemDestroyed, feed)
	}
}

// testChatMembershipUnique verifies the (conversation_id, principal_id)
// uniqueness constraint surfaces ErrConflict.
func testChatMembershipUnique(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-mem-unique@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "u",
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: p.ID, Role: store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: p.ID, Role: store.ChatRoleAdmin,
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second insert = %v, want ErrConflict", err)
	}
}

// testChatMembershipList verifies ListChatMembershipsByConversation /
// ByPrincipal both surface the same row.
func testChatMembershipList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "chat-mem-alice@example.com")
	bob := mustInsertPrincipal(t, s, "chat-mem-bob@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: alice.ID, Name: "team",
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: alice.ID, Role: store.ChatRoleAdmin,
	}); err != nil {
		t.Fatalf("Insert alice membership: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: bob.ID, Role: store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("Insert bob membership: %v", err)
	}
	byConv, err := s.Meta().ListChatMembershipsByConversation(ctx, cid)
	if err != nil {
		t.Fatalf("ListChatMembershipsByConversation: %v", err)
	}
	if len(byConv) != 2 {
		t.Fatalf("byConv = %d, want 2", len(byConv))
	}
	byBob, err := s.Meta().ListChatMembershipsByPrincipal(ctx, bob.ID)
	if err != nil {
		t.Fatalf("ListChatMembershipsByPrincipal: %v", err)
	}
	if len(byBob) != 1 || byBob[0].PrincipalID != bob.ID || byBob[0].ConversationID != cid {
		t.Fatalf("byBob = %+v, want bob/conv %d", byBob, cid)
	}
}

// testChatMembershipMute round-trips IsMuted / NotificationsSetting /
// MuteUntil through the update path.
func testChatMembershipMute(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-mute@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "mute",
	})
	mid, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: p.ID, Role: store.ChatRoleAdmin,
	})
	if err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}
	cur, err := s.Meta().GetChatMembership(ctx, cid, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	until := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cur.IsMuted = true
	cur.MuteUntil = &until
	cur.NotificationsSetting = store.ChatNotificationsMentions
	if err := s.Meta().UpdateChatMembership(ctx, cur); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Meta().GetChatMembership(ctx, cid, p.ID)
	if !got.IsMuted || got.MuteUntil == nil || !got.MuteUntil.Equal(until) ||
		got.NotificationsSetting != store.ChatNotificationsMentions {
		t.Fatalf("mute round-trip: %+v", got)
	}
	// Unmute clears.
	got.IsMuted = false
	got.MuteUntil = nil
	got.NotificationsSetting = store.ChatNotificationsAll
	if err := s.Meta().UpdateChatMembership(ctx, got); err != nil {
		t.Fatalf("Update unmute: %v", err)
	}
	final, _ := s.Meta().GetChatMembership(ctx, cid, p.ID)
	if final.IsMuted || final.MuteUntil != nil ||
		final.NotificationsSetting != store.ChatNotificationsAll {
		t.Fatalf("unmute round-trip: %+v", final)
	}
	if final.ID != mid {
		t.Fatalf("ID drift: got %d want %d", final.ID, mid)
	}
}

// testChatMembershipLastRead verifies SetLastRead + LastReadAt round-trip
// the per-membership read pointer.
func testChatMembershipLastRead(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-lastread@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "lr",
	})
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: p.ID, Role: store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}
	pidArg := p.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg, BodyText: "hi",
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}
	got, joined, err := s.Meta().LastReadAt(ctx, p.ID, cid)
	if err != nil {
		t.Fatalf("LastReadAt: %v", err)
	}
	if got != nil {
		t.Fatalf("initial LastReadAt should be nil, got %v", *got)
	}
	if joined.IsZero() {
		t.Fatalf("JoinedAt should not be zero")
	}
	if err := s.Meta().SetLastRead(ctx, p.ID, cid, mid); err != nil {
		t.Fatalf("SetLastRead: %v", err)
	}
	got, _, err = s.Meta().LastReadAt(ctx, p.ID, cid)
	if err != nil {
		t.Fatalf("LastReadAt after set: %v", err)
	}
	if got == nil || *got != mid {
		t.Fatalf("LastReadAt = %v, want %d", got, mid)
	}
}

// testChatMessageInsertGet covers a basic round-trip of every
// non-derived field plus the conversation's denormalised LastMessageAt
// + MessageCount advance.
func testChatMessageInsertGet(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-msg-rt@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "rt",
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	pidArg := p.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &pidArg,
		BodyText:          "hello world",
		BodyHTML:          "<p>hello world</p>",
		BodyFormat:        store.ChatBodyFormatHTML,
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}
	got, err := s.Meta().GetChatMessage(ctx, mid)
	if err != nil {
		t.Fatalf("GetChatMessage: %v", err)
	}
	if got.BodyText != "hello world" || got.BodyHTML != "<p>hello world</p>" ||
		got.BodyFormat != store.ChatBodyFormatHTML {
		t.Fatalf("message body mismatch: %+v", got)
	}
	if got.SenderPrincipalID == nil || *got.SenderPrincipalID != p.ID {
		t.Fatalf("sender principal not preserved: %+v", got.SenderPrincipalID)
	}
	if got.IsSystem || got.DeletedAt != nil || got.EditedAt != nil {
		t.Fatalf("flag fields should be defaults: %+v", got)
	}
	if got.ModSeq != 1 {
		t.Fatalf("initial ModSeq = %d, want 1", got.ModSeq)
	}
	conv, _ := s.Meta().GetChatConversation(ctx, cid)
	if conv.MessageCount != 1 || conv.LastMessageAt == nil {
		t.Fatalf("conversation not advanced: %+v", conv)
	}
}

// testChatMessageListTimeWindow exercises the CreatedAfter /
// CreatedBefore predicates plus the IncludeDeleted toggle.
func testChatMessageListTimeWindow(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-msg-window@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "win",
	})
	pidArg := p.ID
	for i := 0; i < 3; i++ {
		if _, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
			ConversationID: cid, SenderPrincipalID: &pidArg,
			BodyText: fmt.Sprintf("m%d", i),
		}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := s.Meta().ListChatMessages(ctx, store.ChatMessageFilter{
		ConversationID: &cid,
	})
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Soft-delete the middle message and verify the default filter
	// hides it.
	if err := s.Meta().SoftDeleteChatMessage(ctx, got[1].ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	live, _ := s.Meta().ListChatMessages(ctx, store.ChatMessageFilter{ConversationID: &cid})
	if len(live) != 2 {
		t.Fatalf("live len = %d, want 2", len(live))
	}
	all, _ := s.Meta().ListChatMessages(ctx, store.ChatMessageFilter{
		ConversationID: &cid, IncludeDeleted: true,
	})
	if len(all) != 3 {
		t.Fatalf("with-deleted len = %d, want 3", len(all))
	}
}

// testChatMessageUpdateEdit verifies UpdateChatMessage advances ModSeq
// and persists EditedAt.
func testChatMessageUpdateEdit(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-msg-edit@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "e",
	})
	pidArg := p.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg, BodyText: "draft",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	cur, _ := s.Meta().GetChatMessage(ctx, mid)
	prior := cur.ModSeq
	cur.BodyText = "final"
	editedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cur.EditedAt = &editedAt
	if err := s.Meta().UpdateChatMessage(ctx, cur); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Meta().GetChatMessage(ctx, mid)
	if got.ModSeq <= prior {
		t.Fatalf("ModSeq stuck: %d -> %d", prior, got.ModSeq)
	}
	if got.BodyText != "final" || got.EditedAt == nil || !got.EditedAt.Equal(editedAt) {
		t.Fatalf("edit not persisted: %+v", got)
	}
}

// testChatMessageSoftDelete soft-deletes a message and verifies the
// row stays visible under IncludeDeleted but with cleared bodies.
func testChatMessageSoftDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-msg-del@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "del",
	})
	pidArg := p.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg,
		BodyText: "secret", BodyHTML: "<i>secret</i>",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().SoftDeleteChatMessage(ctx, mid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// Idempotent.
	if err := s.Meta().SoftDeleteChatMessage(ctx, mid); err != nil {
		t.Fatalf("SoftDelete twice: %v", err)
	}
	got, err := s.Meta().GetChatMessage(ctx, mid)
	if err != nil {
		t.Fatalf("GetChatMessage after soft-delete: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatalf("DeletedAt not set: %+v", got)
	}
	if got.BodyText != "" || got.BodyHTML != "" {
		t.Fatalf("body not cleared: %+v", got)
	}
}

// testChatMessageAttachmentsShape rejects malformed attachments and
// accepts a well-formed one.
func testChatMessageAttachmentsShape(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-msg-att@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "a",
	})
	pidArg := p.ID
	good := []byte(`[{"blob_hash":"abc","content_type":"image/png","filename":"a.png","size":42}]`)
	if _, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg,
		BodyText: "x", AttachmentsJSON: good,
	}); err != nil {
		t.Fatalf("good attachment: %v", err)
	}
	bad := []byte(`[{"content_type":"image/png"}]`)
	if _, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg,
		BodyText: "y", AttachmentsJSON: bad,
	}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("bad attachment = %v, want ErrInvalidArgument", err)
	}
}

// testChatMessageReactionsShape exercises the reaction-cap validators
// at insert time.
func testChatMessageReactionsShape(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-msg-react@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "r",
	})
	pidArg := p.ID
	good := []byte(`{"👍":[42,17]}`)
	if _, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg,
		BodyText: "x", ReactionsJSON: good,
	}); err != nil {
		t.Fatalf("good reactions: %v", err)
	}
	bad := []byte(`{"<script>x":[1]}`)
	if _, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg,
		BodyText: "y", ReactionsJSON: bad,
	}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("bad reactions = %v, want ErrInvalidArgument", err)
	}
}

// testChatReactionAddRemove verifies SetChatReaction adds and removes
// a principal atomically.
func testChatReactionAddRemove(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-react-toggle@example.com")
	cid, _ := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: p.ID, Name: "t",
	})
	pidArg := p.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg, BodyText: "x",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	thumbs := "👍"
	if err := s.Meta().SetChatReaction(ctx, mid, thumbs, p.ID, true); err != nil {
		t.Fatalf("SetChatReaction add: %v", err)
	}
	got, _ := s.Meta().GetChatMessage(ctx, mid)
	if !bytes.Contains(got.ReactionsJSON, []byte(thumbs)) ||
		!bytes.Contains(got.ReactionsJSON, []byte(fmt.Sprintf("%d", p.ID))) {
		t.Fatalf("after add reactions = %s", got.ReactionsJSON)
	}
	// Idempotent re-add.
	if err := s.Meta().SetChatReaction(ctx, mid, thumbs, p.ID, true); err != nil {
		t.Fatalf("SetChatReaction re-add: %v", err)
	}
	if err := s.Meta().SetChatReaction(ctx, mid, thumbs, p.ID, false); err != nil {
		t.Fatalf("SetChatReaction remove: %v", err)
	}
	got, _ = s.Meta().GetChatMessage(ctx, mid)
	if len(got.ReactionsJSON) != 0 {
		t.Fatalf("after remove reactions should be empty, got %q", got.ReactionsJSON)
	}
}

// testChatBlockInsertListIsBlocked exercises the basic block-list
// surface: insert, list, IsBlocked, delete.
func testChatBlockInsertListIsBlocked(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "chat-block-alice@example.com")
	bob := mustInsertPrincipal(t, s, "chat-block-bob@example.com")
	if err := s.Meta().InsertChatBlock(ctx, store.ChatBlock{
		BlockerPrincipalID: alice.ID, BlockedPrincipalID: bob.ID,
		Reason: "spam",
	}); err != nil {
		t.Fatalf("InsertChatBlock: %v", err)
	}
	yes, err := s.Meta().IsBlocked(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !yes {
		t.Fatalf("IsBlocked = false, want true")
	}
	// Reverse direction does not auto-block.
	rev, _ := s.Meta().IsBlocked(ctx, bob.ID, alice.ID)
	if rev {
		t.Fatalf("reverse IsBlocked = true, want false (block is one-way)")
	}
	got, err := s.Meta().ListChatBlocksBy(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListChatBlocksBy: %v", err)
	}
	if len(got) != 1 || got[0].BlockedPrincipalID != bob.ID || got[0].Reason != "spam" {
		t.Fatalf("ListChatBlocksBy: %+v", got)
	}
	if err := s.Meta().DeleteChatBlock(ctx, alice.ID, bob.ID); err != nil {
		t.Fatalf("DeleteChatBlock: %v", err)
	}
	if err := s.Meta().DeleteChatBlock(ctx, alice.ID, bob.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteChatBlock twice = %v, want ErrNotFound", err)
	}
}

// testChatBlockRejectsSelf rejects a block where blocker == blocked.
func testChatBlockRejectsSelf(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-self-block@example.com")
	if err := s.Meta().InsertChatBlock(ctx, store.ChatBlock{
		BlockerPrincipalID: p.ID, BlockedPrincipalID: p.ID,
	}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("self-block = %v, want ErrInvalidArgument", err)
	}
}

// testDeletePrincipalCascadesChat confirms DeletePrincipal sweeps
// memberships and blocks belonging to the principal, and nulls the
// sender of messages they had authored.
func testDeletePrincipalCascadesChat(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "chat-del-alice@example.com")
	bob := mustInsertPrincipal(t, s, "chat-del-bob@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, CreatedByPrincipalID: bob.ID, Name: "d",
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	memID, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: alice.ID, Role: store.ChatRoleMember,
	})
	if err != nil {
		t.Fatalf("InsertChatMembership: %v", err)
	}
	pidArg := alice.ID
	mid, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID: cid, SenderPrincipalID: &pidArg, BodyText: "from alice",
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}
	if err := s.Meta().InsertChatBlock(ctx, store.ChatBlock{
		BlockerPrincipalID: alice.ID, BlockedPrincipalID: bob.ID,
	}); err != nil {
		t.Fatalf("InsertChatBlock: %v", err)
	}
	if err := s.Meta().DeletePrincipal(ctx, alice.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	// Membership gone.
	if _, err := s.Meta().GetChatMembership(ctx, cid, alice.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("membership survived: %v", err)
	}
	// Block gone.
	yes, _ := s.Meta().IsBlocked(ctx, alice.ID, bob.ID)
	if yes {
		t.Fatalf("block survived DeletePrincipal")
	}
	// Message survives, sender nulled.
	got, err := s.Meta().GetChatMessage(ctx, mid)
	if err != nil {
		t.Fatalf("GetChatMessage: %v", err)
	}
	if got.SenderPrincipalID != nil {
		t.Fatalf("SenderPrincipalID not nulled after DeletePrincipal: %v", *got.SenderPrincipalID)
	}
	_ = memID
}

// -- Wave 2.9.6 chat features (REQ-CHAT-20/32/92) --------------------

// testChatAccountSettingsDefaults verifies that GetChatAccountSettings
// for a principal with no persisted row returns the operator defaults
// without a not-found error (REQ-CHAT-20: 15 min edit window;
// REQ-CHAT-92: never expire).
func testChatAccountSettingsDefaults(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "settings-default@example.com")
	got, err := s.Meta().GetChatAccountSettings(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetChatAccountSettings: %v", err)
	}
	if got.PrincipalID != p.ID {
		t.Errorf("PrincipalID = %d, want %d", got.PrincipalID, p.ID)
	}
	if got.DefaultRetentionSeconds != store.ChatDefaultRetentionSeconds {
		t.Errorf("DefaultRetentionSeconds = %d, want %d",
			got.DefaultRetentionSeconds, store.ChatDefaultRetentionSeconds)
	}
	if got.DefaultEditWindowSeconds != store.ChatDefaultEditWindowSeconds {
		t.Errorf("DefaultEditWindowSeconds = %d, want %d",
			got.DefaultEditWindowSeconds, store.ChatDefaultEditWindowSeconds)
	}
}

// testChatAccountSettingsUpsertRoundTrip exercises the upsert path:
// first insert seeds a row, second call updates it; both reads return
// the latest values.
func testChatAccountSettingsUpsertRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "settings-upsert@example.com")
	if err := s.Meta().UpsertChatAccountSettings(ctx, store.ChatAccountSettings{
		PrincipalID:              p.ID,
		DefaultRetentionSeconds:  3600,
		DefaultEditWindowSeconds: 60,
	}); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	got, err := s.Meta().GetChatAccountSettings(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after insert: %v", err)
	}
	if got.DefaultRetentionSeconds != 3600 || got.DefaultEditWindowSeconds != 60 {
		t.Errorf("after insert: %+v", got)
	}
	// Update.
	if err := s.Meta().UpsertChatAccountSettings(ctx, store.ChatAccountSettings{
		PrincipalID:              p.ID,
		DefaultRetentionSeconds:  7200,
		DefaultEditWindowSeconds: 0,
	}); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got, err = s.Meta().GetChatAccountSettings(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.DefaultRetentionSeconds != 7200 || got.DefaultEditWindowSeconds != 0 {
		t.Errorf("after update: %+v", got)
	}
}

// testChatConversationRetentionEditWindowRoundTrip verifies that
// RetentionSeconds / EditWindowSeconds / ReadReceiptsEnabled survive
// an Insert / Get pair and an Update / Get pair.
func testChatConversationRetentionEditWindowRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "convopts@example.com")
	retention := int64(3600)
	editWin := int64(120)
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "policy-space",
		CreatedByPrincipalID: p.ID,
		RetentionSeconds:     &retention,
		EditWindowSeconds:    &editWin,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	got, err := s.Meta().GetChatConversation(ctx, cid)
	if err != nil {
		t.Fatalf("GetChatConversation: %v", err)
	}
	if got.RetentionSeconds == nil || *got.RetentionSeconds != retention {
		t.Errorf("RetentionSeconds = %v, want %d", got.RetentionSeconds, retention)
	}
	if got.EditWindowSeconds == nil || *got.EditWindowSeconds != editWin {
		t.Errorf("EditWindowSeconds = %v, want %d", got.EditWindowSeconds, editWin)
	}
	if !got.ReadReceiptsEnabled {
		t.Errorf("ReadReceiptsEnabled defaulted to false; want true")
	}
	// Update: clear retention, change edit window, disable receipts.
	got.RetentionSeconds = nil
	newWin := int64(60)
	got.EditWindowSeconds = &newWin
	got.ReadReceiptsEnabled = false
	if err := s.Meta().UpdateChatConversation(ctx, got); err != nil {
		t.Fatalf("UpdateChatConversation: %v", err)
	}
	got2, err := s.Meta().GetChatConversation(ctx, cid)
	if err != nil {
		t.Fatalf("GetChatConversation post-update: %v", err)
	}
	if got2.RetentionSeconds != nil {
		t.Errorf("RetentionSeconds after update = %v, want nil", got2.RetentionSeconds)
	}
	if got2.EditWindowSeconds == nil || *got2.EditWindowSeconds != newWin {
		t.Errorf("EditWindowSeconds after update = %v, want %d", got2.EditWindowSeconds, newWin)
	}
	if got2.ReadReceiptsEnabled {
		t.Errorf("ReadReceiptsEnabled after disable = true")
	}
}

// testChatRetentionHardDeleteAndRecount inserts five messages, hard-
// deletes the most recent two, and asserts the conversation's
// MessageCount and LastMessageAt are recomputed against the surviving
// rows.
func testChatRetentionHardDeleteAndRecount(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "retention-recount@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "rec",
		CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	pidArg := p.ID
	ids := make([]store.ChatMessageID, 0, 5)
	for i := 0; i < 5; i++ {
		id, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
			ConversationID:    cid,
			SenderPrincipalID: &pidArg,
			BodyText:          "msg",
			BodyFormat:        store.ChatBodyFormatText,
		})
		if err != nil {
			t.Fatalf("InsertChatMessage[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}
	// Hard-delete the two most recent rows.
	for _, id := range ids[3:] {
		if err := s.Meta().HardDeleteChatMessage(ctx, id); err != nil {
			t.Fatalf("HardDeleteChatMessage(%d): %v", id, err)
		}
	}
	// Conversation now has 3 live rows; LastMessageAt = most recent
	// surviving row's CreatedAt.
	conv, err := s.Meta().GetChatConversation(ctx, cid)
	if err != nil {
		t.Fatalf("GetChatConversation: %v", err)
	}
	if conv.MessageCount != 3 {
		t.Errorf("MessageCount = %d, want 3", conv.MessageCount)
	}
	if conv.LastMessageAt == nil {
		t.Errorf("LastMessageAt = nil, want survivor's CreatedAt")
	}
	// HardDelete on an unknown id returns ErrNotFound.
	if err := s.Meta().HardDeleteChatMessage(ctx, store.ChatMessageID(999999)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("HardDeleteChatMessage(missing) err = %v, want ErrNotFound", err)
	}
}

// testChatRetentionListConversationsForRetention exercises the cursor
// + filter on the retention sweeper's listing path. Conversations
// without a positive RetentionSeconds must be excluded; with one,
// included; ordering is by ID ascending.
func testChatRetentionListConversationsForRetention(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "retention-list@example.com")
	// One with no retention (NULL).
	if _, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "no-retention",
		CreatedByPrincipalID: p.ID,
	}); err != nil {
		t.Fatalf("Insert no-retention: %v", err)
	}
	// One with retention=0 (never expire — must be excluded).
	zero := int64(0)
	if _, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "zero-retention",
		CreatedByPrincipalID: p.ID,
		RetentionSeconds:     &zero,
	}); err != nil {
		t.Fatalf("Insert zero-retention: %v", err)
	}
	// One with retention=3600 (should be returned).
	one := int64(3600)
	wantID, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "with-retention",
		CreatedByPrincipalID: p.ID,
		RetentionSeconds:     &one,
	})
	if err != nil {
		t.Fatalf("Insert with-retention: %v", err)
	}
	out, err := s.Meta().ListChatConversationsForRetention(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListChatConversationsForRetention: %v", err)
	}
	found := false
	for _, c := range out {
		if c.ID == wantID {
			found = true
			if c.RetentionSeconds == nil || *c.RetentionSeconds != 3600 {
				t.Errorf("returned row RetentionSeconds = %v, want 3600", c.RetentionSeconds)
			}
		}
	}
	if !found {
		t.Errorf("wantID %d not in retention list: %+v", wantID, out)
	}
}

// testChatAttachmentBlobRefcountOnHardDelete exercises Track B of Wave
// 2.9.7: chat attachments increment blob_refs.ref_count on insert and
// decrement on hard-delete, atomically with the row mutation. Two
// messages share one common attachment hash and each carry one unique
// hash; hard-deleting a message drops the shared count by one and
// drains the unique count to zero. Mirrors the mail-side
// expunge / mailbox-delete refcount path.
func testChatAttachmentBlobRefcountOnHardDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-att-refcount@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "att",
		CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	// Three deterministic 64-char hashes; the wire layer produces
	// hex-BLAKE3 here but the metadata layer treats blob_hash as opaque.
	shared := strings.Repeat("a", 64)
	uniq1 := strings.Repeat("b", 64)
	uniq2 := strings.Repeat("c", 64)
	pidArg := p.ID
	mkAtts := func(hashes ...string) []byte {
		var sb strings.Builder
		sb.WriteByte('[')
		for i, h := range hashes {
			if i > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb,
				`{"blob_hash":%q,"content_type":"application/octet-stream","filename":"f%d.bin","size":%d}`,
				h, i, 100+i)
		}
		sb.WriteByte(']')
		return []byte(sb.String())
	}
	id1, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &pidArg,
		BodyText:          "msg1",
		BodyFormat:        store.ChatBodyFormatText,
		AttachmentsJSON:   mkAtts(shared, uniq1),
	})
	if err != nil {
		t.Fatalf("InsertChatMessage 1: %v", err)
	}
	id2, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &pidArg,
		BodyText:          "msg2",
		BodyFormat:        store.ChatBodyFormatText,
		AttachmentsJSON:   mkAtts(shared, uniq2),
	})
	if err != nil {
		t.Fatalf("InsertChatMessage 2: %v", err)
	}
	wantRef := func(label, hash string, want int64) {
		t.Helper()
		_, n, err := s.Meta().GetBlobRef(ctx, hash)
		if err != nil {
			t.Fatalf("GetBlobRef(%s) %s: %v", label, hash, err)
		}
		if n != want {
			t.Errorf("GetBlobRef(%s) ref_count = %d, want %d", label, n, want)
		}
	}
	// After both inserts: shared has two refs, each unique has one.
	wantRef("after-both-inserts shared", shared, 2)
	wantRef("after-both-inserts uniq1", uniq1, 1)
	wantRef("after-both-inserts uniq2", uniq2, 1)
	// Hard-delete msg1: shared drops to one ref, uniq1 drains to zero,
	// uniq2 unaffected.
	if err := s.Meta().HardDeleteChatMessage(ctx, id1); err != nil {
		t.Fatalf("HardDeleteChatMessage(1): %v", err)
	}
	wantRef("after-delete-1 shared", shared, 1)
	wantRef("after-delete-1 uniq1", uniq1, 0)
	wantRef("after-delete-1 uniq2", uniq2, 1)
	// Hard-delete msg2: shared drains to zero, uniq2 drains to zero.
	if err := s.Meta().HardDeleteChatMessage(ctx, id2); err != nil {
		t.Fatalf("HardDeleteChatMessage(2): %v", err)
	}
	wantRef("after-delete-2 shared", shared, 0)
	wantRef("after-delete-2 uniq1", uniq1, 0)
	wantRef("after-delete-2 uniq2", uniq2, 0)
}

// testChatAttachmentDecRefUnderflowGuard exercises the Wave 2.9.9 Track
// A guard on the metadata backends' decRef helper. Once the public
// hard-delete path has driven a hash's blob_refs.ref_count to zero,
// any subsequent attempt to decrement it again must be a no-op: the
// SQL "WHERE ref_count > 0" guard prevents underflow on SQLite (signed
// INTEGER wrap-around) and Postgres (negative bigint), and the
// fakestore clamps in memory by construction. The test exercises the
// path with a balanced insert / hard-delete cycle, asserts the count
// settles at zero, then drives a second hard-delete on the same
// message ID — that returns ErrNotFound (graceful) and must not change
// the row's ref_count. We additionally re-run a fresh insert / delete
// cycle to confirm subsequent inserts are still respected, which would
// not be the case if the underflow guard had wrongly clamped a
// pre-existing positive count.
func testChatAttachmentDecRefUnderflowGuard(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "chat-att-underflow@example.com")
	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind: store.ChatConversationKindSpace, Name: "underflow",
		CreatedByPrincipalID: p.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	hash := strings.Repeat("d", 64)
	pidArg := p.ID
	atts := []byte(`[{"blob_hash":"` + hash + `","content_type":"application/octet-stream","filename":"a.bin","size":42}]`)
	get := func(label string) int64 {
		t.Helper()
		_, n, err := s.Meta().GetBlobRef(ctx, hash)
		if err != nil {
			// A never-registered hash returns ErrNotFound; treat as 0.
			if errors.Is(err, store.ErrNotFound) {
				return 0
			}
			t.Fatalf("GetBlobRef %s: %v", label, err)
		}
		return n
	}
	// First cycle: insert and hard-delete drive the count to 0.
	id1, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &pidArg,
		BodyText:          "first",
		BodyFormat:        store.ChatBodyFormatText,
		AttachmentsJSON:   atts,
	})
	if err != nil {
		t.Fatalf("InsertChatMessage 1: %v", err)
	}
	if got := get("after-insert-1"); got != 1 {
		t.Fatalf("ref_count after insert 1 = %d, want 1", got)
	}
	if err := s.Meta().HardDeleteChatMessage(ctx, id1); err != nil {
		t.Fatalf("HardDeleteChatMessage 1: %v", err)
	}
	if got := get("after-delete-1"); got != 0 {
		t.Fatalf("ref_count after delete 1 = %d, want 0", got)
	}
	// Second hard-delete on the same message ID: the row is gone, so
	// the metadata layer returns ErrNotFound before reaching decRef.
	// The row's ref_count must remain at zero either way (no panic, no
	// underflow). We catch the entire returned error space — some
	// backends may surface the missing row with a more specific
	// sentinel — and only insist on "not nil" + "count unchanged".
	if err := s.Meta().HardDeleteChatMessage(ctx, id1); err == nil {
		t.Fatalf("second HardDeleteChatMessage on already-deleted id returned nil")
	}
	if got := get("after-double-delete"); got != 0 {
		t.Fatalf("ref_count after double delete = %d, want 0 (no underflow)", got)
	}
	// Third cycle: a fresh insert+delete must still drive the count
	// 0 -> 1 -> 0 cleanly. If the underflow guard had wrongly clamped
	// the pre-existing row, the increment would be lost and the
	// post-insert count would still read 0.
	id2, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &pidArg,
		BodyText:          "second",
		BodyFormat:        store.ChatBodyFormatText,
		AttachmentsJSON:   atts,
	})
	if err != nil {
		t.Fatalf("InsertChatMessage 2: %v", err)
	}
	if got := get("after-insert-2"); got != 1 {
		t.Fatalf("ref_count after re-insert = %d, want 1", got)
	}
	if err := s.Meta().HardDeleteChatMessage(ctx, id2); err != nil {
		t.Fatalf("HardDeleteChatMessage 2: %v", err)
	}
	if got := get("after-delete-2"); got != 0 {
		t.Fatalf("ref_count after final delete = %d, want 0", got)
	}
}

// -- re #47: per-member change-feed fanout ----------------------------

// testChatChangeFeedInsertMessageFansToAllMembers verifies that
// InsertChatMessage appends a state-change row to EVERY member's change
// feed, not just the conversation creator. Concretely: alice creates a
// DM, both alice and bob are members, bob sends a message — alice's feed
// must contain the new-message entry without a manual reload.
func testChatChangeFeedInsertMessageFansToAllMembers(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "fanout-alice@example.com")
	bob := mustInsertPrincipal(t, s, "fanout-bob@example.com")

	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindDM,
		CreatedByPrincipalID: alice.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: alice.ID, Role: store.ChatRoleAdmin,
	}); err != nil {
		t.Fatalf("InsertChatMembership alice: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: bob.ID, Role: store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("InsertChatMembership bob: %v", err)
	}

	// Drain the feeds so we have a clean baseline.
	aliceFeedPre, err := s.Meta().ReadChangeFeed(ctx, alice.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed alice pre: %v", err)
	}
	var aliceCursor store.ChangeSeq
	if len(aliceFeedPre) > 0 {
		aliceCursor = aliceFeedPre[len(aliceFeedPre)-1].Seq
	}
	bobFeedPre, err := s.Meta().ReadChangeFeed(ctx, bob.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed bob pre: %v", err)
	}
	var bobCursor store.ChangeSeq
	if len(bobFeedPre) > 0 {
		bobCursor = bobFeedPre[len(bobFeedPre)-1].Seq
	}

	// Bob sends a message.
	bobPID := bob.ID
	msgID, err := s.Meta().InsertChatMessage(ctx, store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &bobPID,
		BodyText:          "hello alice",
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}

	// Alice's feed must contain a Created entry for the new message.
	aliceTail, err := s.Meta().ReadChangeFeed(ctx, alice.ID, aliceCursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed alice tail: %v", err)
	}
	var aliceSawMsg bool
	for _, e := range aliceTail {
		if e.Kind == store.EntityKindChatMessage && e.EntityID == uint64(msgID) &&
			e.Op == store.ChangeOpCreated {
			aliceSawMsg = true
		}
	}
	if !aliceSawMsg {
		t.Fatalf("alice's change feed missing new-message entry: tail=%+v", aliceTail)
	}

	// Bob's feed must also contain a Created entry.
	bobTail, err := s.Meta().ReadChangeFeed(ctx, bob.ID, bobCursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed bob tail: %v", err)
	}
	var bobSawMsg bool
	for _, e := range bobTail {
		if e.Kind == store.EntityKindChatMessage && e.EntityID == uint64(msgID) &&
			e.Op == store.ChangeOpCreated {
			bobSawMsg = true
		}
	}
	if !bobSawMsg {
		t.Fatalf("bob's change feed missing new-message entry: tail=%+v", bobTail)
	}
}

// testChatChangeFeedInsertMembershipFansToExistingMembers verifies that
// when a new member joins an existing conversation, all pre-existing
// members receive a Membership/Created state-change row so their
// conversation list refreshes without a manual reload (re #47).
func testChatChangeFeedInsertMembershipFansToExistingMembers(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "fanout-mem-alice@example.com")
	bob := mustInsertPrincipal(t, s, "fanout-mem-bob@example.com")
	carol := mustInsertPrincipal(t, s, "fanout-mem-carol@example.com")

	cid, err := s.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 store.ChatConversationKindSpace,
		Name:                 "team",
		CreatedByPrincipalID: alice.ID,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: alice.ID, Role: store.ChatRoleAdmin,
	}); err != nil {
		t.Fatalf("InsertChatMembership alice: %v", err)
	}
	// Bob is also already in the conversation.
	if _, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: bob.ID, Role: store.ChatRoleMember,
	}); err != nil {
		t.Fatalf("InsertChatMembership bob: %v", err)
	}

	// Drain alice's and bob's feeds.
	aliceFeedPre, _ := s.Meta().ReadChangeFeed(ctx, alice.ID, 0, 1000)
	var aliceCursor store.ChangeSeq
	if len(aliceFeedPre) > 0 {
		aliceCursor = aliceFeedPre[len(aliceFeedPre)-1].Seq
	}
	bobFeedPre, _ := s.Meta().ReadChangeFeed(ctx, bob.ID, 0, 1000)
	var bobCursor store.ChangeSeq
	if len(bobFeedPre) > 0 {
		bobCursor = bobFeedPre[len(bobFeedPre)-1].Seq
	}

	// Carol joins.
	carolMemID, err := s.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID: cid, PrincipalID: carol.ID, Role: store.ChatRoleMember,
	})
	if err != nil {
		t.Fatalf("InsertChatMembership carol: %v", err)
	}

	// Alice must see the new membership in her feed.
	aliceTail, err := s.Meta().ReadChangeFeed(ctx, alice.ID, aliceCursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed alice tail: %v", err)
	}
	var aliceSawMem bool
	for _, e := range aliceTail {
		if e.Kind == store.EntityKindMembership && e.EntityID == uint64(carolMemID) &&
			e.Op == store.ChangeOpCreated {
			aliceSawMem = true
		}
	}
	if !aliceSawMem {
		t.Fatalf("alice's change feed missing carol's membership entry: tail=%+v", aliceTail)
	}

	// Bob must also see it.
	bobTail, err := s.Meta().ReadChangeFeed(ctx, bob.ID, bobCursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed bob tail: %v", err)
	}
	var bobSawMem bool
	for _, e := range bobTail {
		if e.Kind == store.EntityKindMembership && e.EntityID == uint64(carolMemID) &&
			e.Op == store.ChangeOpCreated {
			bobSawMem = true
		}
	}
	if !bobSawMem {
		t.Fatalf("bob's change feed missing carol's membership entry: tail=%+v", bobTail)
	}

	// Carol herself must also see her own membership in her feed.
	carolTail, err := s.Meta().ReadChangeFeed(ctx, carol.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed carol: %v", err)
	}
	var carolSawMem bool
	for _, e := range carolTail {
		if e.Kind == store.EntityKindMembership && e.EntityID == uint64(carolMemID) &&
			e.Op == store.ChangeOpCreated {
			carolSawMem = true
		}
	}
	if !carolSawMem {
		t.Fatalf("carol's change feed missing her own membership entry: tail=%+v", carolTail)
	}
}

// -- re #47: DM server-side deduplication --------------------------------

// testFindDMBetweenNotFound verifies FindDMBetween returns (_, _, false,
// nil) when no DM exists between the two principals.
func testFindDMBetweenNotFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "dm-notfound-alice@example.com")
	bob := mustInsertPrincipal(t, s, "dm-notfound-bob@example.com")

	_, _, found, err := s.Meta().FindDMBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("FindDMBetween: %v", err)
	}
	if found {
		t.Fatal("FindDMBetween: want false, got true")
	}
}

// testFindDMBetweenFound verifies FindDMBetween returns the conversation
// and its memberships after InsertDMConversation.
func testFindDMBetweenFound(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "dm-found-alice@example.com")
	bob := mustInsertPrincipal(t, s, "dm-found-bob@example.com")

	now := time.Now().Truncate(time.Microsecond)
	c, members, err := s.Meta().InsertDMConversation(ctx, alice.ID, bob.ID, "bob@example.com", now)
	if err != nil {
		t.Fatalf("InsertDMConversation: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("InsertDMConversation returned zero conversation id")
	}
	if len(members) != 2 {
		t.Fatalf("InsertDMConversation members: got %d, want 2", len(members))
	}

	got, gotMembers, found, err := s.Meta().FindDMBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("FindDMBetween: %v", err)
	}
	if !found {
		t.Fatal("FindDMBetween: want true, got false")
	}
	if got.ID != c.ID {
		t.Errorf("FindDMBetween: conversation id = %d, want %d", got.ID, c.ID)
	}
	if len(gotMembers) != 2 {
		t.Errorf("FindDMBetween: members count = %d, want 2", len(gotMembers))
	}
}

// testFindDMBetweenSymmetric verifies FindDMBetween returns the same
// result regardless of argument order.
func testFindDMBetweenSymmetric(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "dm-sym-alice@example.com")
	bob := mustInsertPrincipal(t, s, "dm-sym-bob@example.com")

	now := time.Now().Truncate(time.Microsecond)
	c, _, err := s.Meta().InsertDMConversation(ctx, alice.ID, bob.ID, "bob", now)
	if err != nil {
		t.Fatalf("InsertDMConversation: %v", err)
	}

	// alice -> bob
	got1, _, found1, err := s.Meta().FindDMBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("FindDMBetween alice->bob: %v", err)
	}
	// bob -> alice
	got2, _, found2, err := s.Meta().FindDMBetween(ctx, bob.ID, alice.ID)
	if err != nil {
		t.Fatalf("FindDMBetween bob->alice: %v", err)
	}
	if !found1 || !found2 {
		t.Fatalf("FindDMBetween: found = (%v, %v), both want true", found1, found2)
	}
	if got1.ID != c.ID || got2.ID != c.ID {
		t.Errorf("FindDMBetween ids = (%d, %d), want %d", got1.ID, got2.ID, c.ID)
	}
}

// testInsertDMConversationConflictOnDuplicate verifies that a second
// InsertDMConversation call for the same pair returns ErrConflict.
func testInsertDMConversationConflictOnDuplicate(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "dm-dup-alice@example.com")
	bob := mustInsertPrincipal(t, s, "dm-dup-bob@example.com")

	now := time.Now().Truncate(time.Microsecond)
	_, _, err := s.Meta().InsertDMConversation(ctx, alice.ID, bob.ID, "bob", now)
	if err != nil {
		t.Fatalf("first InsertDMConversation: %v", err)
	}

	_, _, err2 := s.Meta().InsertDMConversation(ctx, alice.ID, bob.ID, "bob", now)
	if !errors.Is(err2, store.ErrConflict) {
		t.Errorf("second InsertDMConversation: want ErrConflict, got %v", err2)
	}
}

// testInsertDMConversationSelfRejected verifies that a self-DM returns
// ErrInvalidArgument.
func testInsertDMConversationSelfRejected(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "dm-self-alice@example.com")

	now := time.Now().Truncate(time.Microsecond)
	_, _, err := s.Meta().InsertDMConversation(ctx, alice.ID, alice.ID, "alice", now)
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("self-DM: want ErrInvalidArgument, got %v", err)
	}
}

// testInsertDMConversationDistinctPairs verifies that DMs between
// different pairs are independent: alice<->bob and alice<->carol produce
// two separate conversation rows.
func testInsertDMConversationDistinctPairs(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	alice := mustInsertPrincipal(t, s, "dm-pairs-alice@example.com")
	bob := mustInsertPrincipal(t, s, "dm-pairs-bob@example.com")
	carol := mustInsertPrincipal(t, s, "dm-pairs-carol@example.com")

	now := time.Now().Truncate(time.Microsecond)
	ab, _, err := s.Meta().InsertDMConversation(ctx, alice.ID, bob.ID, "bob", now)
	if err != nil {
		t.Fatalf("InsertDMConversation alice<->bob: %v", err)
	}
	ac, _, err := s.Meta().InsertDMConversation(ctx, alice.ID, carol.ID, "carol", now)
	if err != nil {
		t.Fatalf("InsertDMConversation alice<->carol: %v", err)
	}
	if ab.ID == ac.ID {
		t.Errorf("alice<->bob and alice<->carol share the same conversation id %d", ab.ID)
	}

	// Confirm FindDMBetween returns the correct row for each pair.
	gotAB, _, foundAB, err := s.Meta().FindDMBetween(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("FindDMBetween alice<->bob: %v", err)
	}
	gotAC, _, foundAC, err := s.Meta().FindDMBetween(ctx, alice.ID, carol.ID)
	if err != nil {
		t.Fatalf("FindDMBetween alice<->carol: %v", err)
	}
	if !foundAB || gotAB.ID != ab.ID {
		t.Errorf("alice<->bob: found=%v id=%d, want %d", foundAB, gotAB.ID, ab.ID)
	}
	if !foundAC || gotAC.ID != ac.ID {
		t.Errorf("alice<->carol: found=%v id=%d, want %d", foundAC, gotAC.ID, ac.ID)
	}
}

// -- Phase 3 Wave 3.8a JMAP PushSubscription (REQ-PROTO-120..122) ----

func testPushSubscriptionInsertGetRoundtrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ps-rt@example.com")
	expires := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	startHour := 22
	endHour := 7
	row := store.PushSubscription{
		PrincipalID:            p.ID,
		DeviceClientID:         "browser-1",
		URL:                    "https://fcm.googleapis.com/fcm/send/abc",
		P256DH:                 []byte("\x04abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ12"),
		Auth:                   []byte("0123456789abcdef"),
		Expires:                &expires,
		Types:                  []string{"Mailbox", "Email"},
		VerificationCode:       "vc-abc",
		Verified:               false,
		VAPIDKeyAtRegistration: "BAS-base64url-public-key",
		NotificationRulesJSON:  []byte(`{"mail":{"categories":["primary"]},"chat":{"dmsAlways":true}}`),
		QuietHoursStartLocal:   &startHour,
		QuietHoursEndLocal:     &endHour,
		QuietHoursTZ:           "Europe/Berlin",
	}
	id, err := s.Meta().InsertPushSubscription(ctx, row)
	if err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}
	if id == 0 {
		t.Fatalf("InsertPushSubscription returned zero id")
	}
	got, err := s.Meta().GetPushSubscription(ctx, id)
	if err != nil {
		t.Fatalf("GetPushSubscription: %v", err)
	}
	if got.PrincipalID != p.ID {
		t.Fatalf("PrincipalID = %d, want %d", got.PrincipalID, p.ID)
	}
	if got.DeviceClientID != "browser-1" || got.URL != "https://fcm.googleapis.com/fcm/send/abc" {
		t.Fatalf("scalar mismatch: %+v", got)
	}
	if string(got.P256DH) != string(row.P256DH) || string(got.Auth) != string(row.Auth) {
		t.Fatalf("key bytes mismatch: %x / %x", got.P256DH, got.Auth)
	}
	if got.Expires == nil || !got.Expires.Equal(expires) {
		t.Fatalf("Expires = %v, want %v", got.Expires, expires)
	}
	if len(got.Types) != 2 || got.Types[0] != "Mailbox" || got.Types[1] != "Email" {
		t.Fatalf("Types = %v", got.Types)
	}
	if got.VerificationCode != "vc-abc" || got.Verified {
		t.Fatalf("verification: code=%q verified=%v", got.VerificationCode, got.Verified)
	}
	if got.VAPIDKeyAtRegistration != "BAS-base64url-public-key" {
		t.Fatalf("vapid key = %q", got.VAPIDKeyAtRegistration)
	}
	if string(got.NotificationRulesJSON) != string(row.NotificationRulesJSON) {
		t.Fatalf("rules JSON = %q", got.NotificationRulesJSON)
	}
	if got.QuietHoursStartLocal == nil || *got.QuietHoursStartLocal != 22 {
		t.Fatalf("qh start = %v", got.QuietHoursStartLocal)
	}
	if got.QuietHoursEndLocal == nil || *got.QuietHoursEndLocal != 7 {
		t.Fatalf("qh end = %v", got.QuietHoursEndLocal)
	}
	if got.QuietHoursTZ != "Europe/Berlin" {
		t.Fatalf("qh tz = %q", got.QuietHoursTZ)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps unset: %+v", got)
	}
	if _, err := s.Meta().GetPushSubscription(ctx, id+9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func testPushSubscriptionListByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ps-list@example.com")
	q := mustInsertPrincipal(t, s, "ps-list-other@example.com")
	for i, pid := range []store.PrincipalID{p.ID, p.ID, q.ID} {
		_, err := s.Meta().InsertPushSubscription(ctx, store.PushSubscription{
			PrincipalID:    pid,
			DeviceClientID: fmt.Sprintf("dev-%d", i),
			URL:            fmt.Sprintf("https://push.example.test/%d", i),
			P256DH:         []byte("p256dh-bytes-65-................................................"),
			Auth:           []byte("0123456789abcdef"),
		})
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, err := s.Meta().ListPushSubscriptionsByPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List p: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List p len = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.PrincipalID != p.ID {
			t.Fatalf("foreign principal leaked: %+v", r)
		}
	}
	if got[0].ID >= got[1].ID {
		t.Fatalf("List not ascending by ID: %d, %d", got[0].ID, got[1].ID)
	}
	got, err = s.Meta().ListPushSubscriptionsByPrincipal(ctx, q.ID)
	if err != nil {
		t.Fatalf("List q: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List q len = %d, want 1", len(got))
	}
}

func testPushSubscriptionUpdate(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ps-up@example.com")
	id, err := s.Meta().InsertPushSubscription(ctx, store.PushSubscription{
		PrincipalID:      p.ID,
		DeviceClientID:   "d",
		URL:              "https://push.example.test/u",
		P256DH:           []byte("65bytes" + strings.Repeat("x", 58)),
		Auth:             []byte("0123456789abcdef"),
		VerificationCode: "vc-1",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	cur, err := s.Meta().GetPushSubscription(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	cur.Verified = true
	cur.VerificationCode = "vc-1"
	cur.Types = []string{"Mailbox"}
	cur.NotificationRulesJSON = []byte(`{"mail":{"categories":["primary"]}}`)
	startHour := 23
	cur.QuietHoursStartLocal = &startHour
	cur.QuietHoursTZ = "UTC"
	if err := s.Meta().UpdatePushSubscription(ctx, cur); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Meta().GetPushSubscription(ctx, id)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !got.Verified {
		t.Fatalf("Verified not set")
	}
	if len(got.Types) != 1 || got.Types[0] != "Mailbox" {
		t.Fatalf("Types = %v", got.Types)
	}
	if string(got.NotificationRulesJSON) != string(cur.NotificationRulesJSON) {
		t.Fatalf("rules JSON not persisted: %q", got.NotificationRulesJSON)
	}
	if got.QuietHoursStartLocal == nil || *got.QuietHoursStartLocal != 23 {
		t.Fatalf("qh start = %v", got.QuietHoursStartLocal)
	}
	if got.QuietHoursTZ != "UTC" {
		t.Fatalf("qh tz = %q", got.QuietHoursTZ)
	}
	// Update on a missing row.
	missing := cur
	missing.ID = id + 9999
	if err := s.Meta().UpdatePushSubscription(ctx, missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing: err = %v, want ErrNotFound", err)
	}
}

func testPushSubscriptionDeleteNotFoundAfter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ps-del@example.com")
	id, err := s.Meta().InsertPushSubscription(ctx, store.PushSubscription{
		PrincipalID:    p.ID,
		DeviceClientID: "d",
		URL:            "https://push.example.test/d",
		P256DH:         []byte("65bytes" + strings.Repeat("y", 58)),
		Auth:           []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeletePushSubscription(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta().GetPushSubscription(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
	if err := s.Meta().DeletePushSubscription(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete twice: err = %v, want ErrNotFound", err)
	}
}

func testPushSubscriptionCascadeOnPrincipalDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ps-casc@example.com")
	id, err := s.Meta().InsertPushSubscription(ctx, store.PushSubscription{
		PrincipalID:    p.ID,
		DeviceClientID: "d",
		URL:            "https://push.example.test/c",
		P256DH:         []byte("65bytes" + strings.Repeat("z", 58)),
		Auth:           []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if _, err := s.Meta().GetPushSubscription(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after principal delete: err = %v, want ErrNotFound", err)
	}
}

// -- Phase 3 Wave 3.9 Email reactions tests ----------------------------

func mustInsertMessage(t *testing.T, s store.Store, mailboxID store.MailboxID, msgID string) store.Message {
	t.Helper()
	// Look up the principal that owns this mailbox so we can set PrincipalID.
	mb, err := s.Meta().GetMailboxByID(ctxT(t), mailboxID)
	if err != nil {
		t.Fatalf("mustInsertMessage: GetMailboxByID(%d): %v", mailboxID, err)
	}
	ref := putBlob(t, s, "From: sender@example.com\r\nMessage-ID: <"+msgID+">\r\n\r\nBody\r\n")
	uid, _, err := s.Meta().InsertMessage(ctxT(t), store.Message{
		PrincipalID:  mb.PrincipalID,
		Size:         ref.Size,
		Blob:         ref,
		ReceivedAt:   time.Now().UTC(),
		InternalDate: time.Now().UTC(),
		Envelope: store.Envelope{
			MessageID: msgID,
			From:      "sender@example.com",
		},
	}, []store.MessageMailbox{{MailboxID: mailboxID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := s.Meta().ListMessages(ctxT(t), mailboxID, store.MessageFilter{Limit: 1000, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m
		}
	}
	t.Fatalf("InsertMessage: UID %d not found in ListMessages", uid)
	return store.Message{}
}

func testEmailReactionAddRemoveIdempotent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "reactor@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "msg001@host")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Add a reaction.
	if err := s.Meta().AddEmailReaction(ctx, msg.ID, "heart", p.ID, now); err != nil {
		t.Fatalf("AddEmailReaction: %v", err)
	}
	// Duplicate add is idempotent.
	if err := s.Meta().AddEmailReaction(ctx, msg.ID, "heart", p.ID, now); err != nil {
		t.Fatalf("AddEmailReaction duplicate: %v", err)
	}
	got, err := s.Meta().ListEmailReactions(ctx, msg.ID)
	if err != nil {
		t.Fatalf("ListEmailReactions: %v", err)
	}
	if _, ok := got["heart"][p.ID]; !ok {
		t.Fatalf("reaction missing after add: got %v", got)
	}
	if len(got["heart"]) != 1 {
		t.Fatalf("expected 1 reactor, got %d", len(got["heart"]))
	}

	// Remove the reaction.
	if err := s.Meta().RemoveEmailReaction(ctx, msg.ID, "heart", p.ID); err != nil {
		t.Fatalf("RemoveEmailReaction: %v", err)
	}
	// Second remove is idempotent.
	if err := s.Meta().RemoveEmailReaction(ctx, msg.ID, "heart", p.ID); err != nil {
		t.Fatalf("RemoveEmailReaction idempotent: %v", err)
	}
	got2, err := s.Meta().ListEmailReactions(ctx, msg.ID)
	if err != nil {
		t.Fatalf("ListEmailReactions after remove: %v", err)
	}
	if len(got2) != 0 {
		t.Fatalf("expected empty reactions, got %v", got2)
	}
}

func testEmailReactionListEmpty(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "empty-react@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "msg-empty@host")

	got, err := s.Meta().ListEmailReactions(ctx, msg.ID)
	if err != nil {
		t.Fatalf("ListEmailReactions empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func testEmailReactionBatchList(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "batch-react@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	m1 := mustInsertMessage(t, s, mb.ID, "batch-msg1@host")
	m2 := mustInsertMessage(t, s, mb.ID, "batch-msg2@host")
	m3 := mustInsertMessage(t, s, mb.ID, "batch-msg3@host")

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Meta().AddEmailReaction(ctx, m1.ID, "thumbsup", p.ID, now); err != nil {
		t.Fatalf("AddEmailReaction m1: %v", err)
	}
	if err := s.Meta().AddEmailReaction(ctx, m2.ID, "heart", p.ID, now); err != nil {
		t.Fatalf("AddEmailReaction m2: %v", err)
	}
	// m3 has no reactions.

	batch, err := s.Meta().BatchListEmailReactions(ctx, []store.MessageID{m1.ID, m2.ID, m3.ID})
	if err != nil {
		t.Fatalf("BatchListEmailReactions: %v", err)
	}
	if _, ok := batch[m1.ID]["thumbsup"][p.ID]; !ok {
		t.Fatalf("m1 thumbsup missing: %v", batch)
	}
	if _, ok := batch[m2.ID]["heart"][p.ID]; !ok {
		t.Fatalf("m2 heart missing: %v", batch)
	}
	if _, ok := batch[m3.ID]; ok {
		t.Fatalf("m3 should not appear in batch: %v", batch)
	}
	// Empty slice returns empty map.
	empty, err := s.Meta().BatchListEmailReactions(ctx, nil)
	if err != nil {
		t.Fatalf("BatchListEmailReactions nil: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty slice: expected empty map, got %v", empty)
	}
}

func testGetMessageByMessageIDHeader(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "msgid-lookup@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	want := mustInsertMessage(t, s, mb.ID, "lookup-001@host")

	got, err := s.Meta().GetMessageByMessageIDHeader(ctx, p.ID, "lookup-001@host")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if got.ID != want.ID {
		t.Fatalf("GetMessageByMessageIDHeader: got ID %d, want %d", got.ID, want.ID)
	}
	// Not found case.
	_, err = s.Meta().GetMessageByMessageIDHeader(ctx, p.ID, "not-here@host")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing msgid: err = %v, want ErrNotFound", err)
	}
}

// -- SearchPrincipalsByText tests -------------------------------------

func testSearchPrincipalsByTextDisplayNameMatch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.com",
		DisplayName: "Alice Wonderland",
	}); err != nil {
		t.Fatalf("insert alice: %v", err)
	}
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "bob@example.com",
		DisplayName: "Bob Builder",
	}); err != nil {
		t.Fatalf("insert bob: %v", err)
	}
	got, err := s.Meta().SearchPrincipalsByText(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	if got[0].CanonicalEmail != "alice@example.com" {
		t.Errorf("got %q, want alice@example.com", got[0].CanonicalEmail)
	}
}

func testSearchPrincipalsByTextEmailLocalPartMatch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "charlie@example.com",
		DisplayName: "Charlie Brown",
	}); err != nil {
		t.Fatalf("insert charlie: %v", err)
	}
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "dave@example.com",
		DisplayName: "Dave Green",
	}); err != nil {
		t.Fatalf("insert dave: %v", err)
	}
	// "cha" prefix matches "charlie" local-part but not "dave".
	got, err := s.Meta().SearchPrincipalsByText(ctx, "cha", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	if got[0].CanonicalEmail != "charlie@example.com" {
		t.Errorf("got %q, want charlie@example.com", got[0].CanonicalEmail)
	}
}

func testSearchPrincipalsByTextSortOrder(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	// Three principals: two with "ali" in the display name (prio 0),
	// one that matches only on the email local-part prefix (prio 1).
	for _, row := range []struct{ email, name string }{
		{"zara@example.com", "Zara Ali"},    // name contains "ali" -> prio 0
		{"alice@example.com", "Alice Ward"}, // name "Alice" contains "ali" -> prio 0
		{"ali-only@example.com", "Plain"},   // only email local-part starts with "ali" -> prio 1
	} {
		if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: row.email, DisplayName: row.name,
		}); err != nil {
			t.Fatalf("insert %s: %v", row.email, err)
		}
	}
	got, err := s.Meta().SearchPrincipalsByText(ctx, "ali", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(got), got)
	}
	// prio 0: "Alice Ward" < "Zara Ali" (alphabetical by lower display name)
	// prio 1: "Plain" (email-local-part match only)
	wantOrder := []string{"alice@example.com", "zara@example.com", "ali-only@example.com"}
	for i, w := range wantOrder {
		if got[i].CanonicalEmail != w {
			t.Errorf("pos %d: got %q, want %q", i, got[i].CanonicalEmail, w)
		}
	}
}

func testSearchPrincipalsByTextLimitClamped(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	for i := 0; i < 5; i++ {
		if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("limituser%d@example.com", i),
			DisplayName:    fmt.Sprintf("Limit User %d", i),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	got, err := s.Meta().SearchPrincipalsByText(ctx, "limit", 3)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3 (limit applied): %+v", len(got), got)
	}
}

func testSearchPrincipalsByTextNoMatch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "nobody@example.com",
		DisplayName: "Nobody",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.Meta().SearchPrincipalsByText(ctx, "zzznomatch", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByText: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d results, want 0", len(got))
	}
}

func testSearchPrincipalsByTextCaseInsensitive(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "francesca@example.com",
		DisplayName: "Francesca",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, prefix := range []string{"Fra", "FRA", "fra", "FRAN"} {
		got, err := s.Meta().SearchPrincipalsByText(ctx, prefix, 10)
		if err != nil {
			t.Fatalf("SearchPrincipalsByText(%q): %v", prefix, err)
		}
		if len(got) != 1 {
			t.Fatalf("SearchPrincipalsByText(%q): got %d results, want 1", prefix, len(got))
		}
	}
}

// -- SearchPrincipalsByTextInDomain tests -----------------------------

// testSearchPrincipalsByTextInDomainEmptyDomain verifies that passing an
// empty domain string is equivalent to calling SearchPrincipalsByText:
// principals from multiple domains all appear.
func testSearchPrincipalsByTextInDomainEmptyDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	for _, row := range []struct{ email, name string }{
		{"carol@alpha.test", "Carol Alpha"},
		{"carol@beta.test", "Carol Beta"},
	} {
		if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: row.email, DisplayName: row.name,
		}); err != nil {
			t.Fatalf("insert %s: %v", row.email, err)
		}
	}
	// empty domain — should see both principals
	got, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "carol", "", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(got), got)
	}
	// whitespace-only domain also counts as empty
	got2, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "carol", "   ", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain(whitespace domain): %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("whitespace domain: got %d results, want 2", len(got2))
	}
}

// testSearchPrincipalsByTextInDomainDomainFilter verifies that only
// principals whose canonical email is in the requested domain are returned.
func testSearchPrincipalsByTextInDomainDomainFilter(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	for _, row := range []struct{ email, name string }{
		{"dave@corp.example", "Dave Corp"},
		{"dave@other.example", "Dave Other"},
		{"eve@corp.example", "Eve Corp"},
	} {
		if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: row.email, DisplayName: row.name,
		}); err != nil {
			t.Fatalf("insert %s: %v", row.email, err)
		}
	}
	got, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "dave", "corp.example", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	if got[0].CanonicalEmail != "dave@corp.example" {
		t.Errorf("got %q, want dave@corp.example", got[0].CanonicalEmail)
	}
}

// testSearchPrincipalsByTextInDomainCaseInsensitiveDomain verifies that
// the domain match is case-insensitive regardless of how the input is cased.
func testSearchPrincipalsByTextInDomainCaseInsensitiveDomain(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "frank@mail.example",
		DisplayName: "Frank",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, domain := range []string{"mail.example", "MAIL.EXAMPLE", "Mail.Example"} {
		got, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "frank", domain, 10)
		if err != nil {
			t.Fatalf("SearchPrincipalsByTextInDomain(domain=%q): %v", domain, err)
		}
		if len(got) != 1 {
			t.Fatalf("domain=%q: got %d results, want 1", domain, len(got))
		}
		if got[0].CanonicalEmail != "frank@mail.example" {
			t.Errorf("domain=%q: got %q, want frank@mail.example", domain, got[0].CanonicalEmail)
		}
	}
}

// testSearchPrincipalsByTextInDomainNoMatch verifies that when no
// principals match both the prefix and the domain, an empty slice (not
// an error) is returned.
func testSearchPrincipalsByTextInDomainNoMatch(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "grace@present.example",
		DisplayName: "Grace",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "grace", "absent.example", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d results, want 0: %+v", len(got), got)
	}
}

// testSearchPrincipalsByTextInDomainEmptyPrefix verifies behaviour when
// prefix is empty but domain is set: all principals in the domain are
// returned (consistent with SearchPrincipalsByText("", ...) which
// returns all principals that match the empty-string LIKE patterns).
func testSearchPrincipalsByTextInDomainEmptyPrefix(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	for _, row := range []struct{ email, name string }{
		{"hank@scoped.test", "Hank Scoped"},
		{"ivan@scoped.test", "Ivan Scoped"},
		{"judy@other.test", "Judy Other"},
	} {
		if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: row.email, DisplayName: row.name,
		}); err != nil {
			t.Fatalf("insert %s: %v", row.email, err)
		}
	}
	got, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "", "scoped.test", 10)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain: %v", err)
	}
	// Both scoped.test principals should appear; judy@other.test must not.
	for _, p := range got {
		if strings.HasSuffix(p.CanonicalEmail, "@other.test") {
			t.Errorf("unexpected cross-domain result: %s", p.CanonicalEmail)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(got), got)
	}
}

// testSearchPrincipalsByTextInDomainLimitRespected verifies that the
// limit parameter caps the result set when there are more matches.
func testSearchPrincipalsByTextInDomainLimitRespected(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	for i := 0; i < 5; i++ {
		if _, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("dlimit%d@dlimit.test", i),
			DisplayName:    fmt.Sprintf("DLimit User %d", i),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	got, err := s.Meta().SearchPrincipalsByTextInDomain(ctx, "dlimit", "dlimit.test", 3)
	if err != nil {
		t.Fatalf("SearchPrincipalsByTextInDomain: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3 (limit applied): %+v", len(got), got)
	}
}
