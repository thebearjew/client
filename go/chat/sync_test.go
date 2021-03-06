package chat

import (
	"testing"
	"time"

	"github.com/keybase/client/go/chat/storage"
	"github.com/keybase/client/go/chat/types"
	"github.com/keybase/client/go/kbtest"
	"github.com/keybase/client/go/protocol/chat1"
	"github.com/keybase/client/go/protocol/gregor1"
	"github.com/keybase/client/go/protocol/keybase1"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
)

func newBlankConv(ctx context.Context, t *testing.T, tc *kbtest.ChatTestContext,
	uid gregor1.UID, ri chat1.RemoteInterface, sender types.Sender, tlfName string) chat1.Conversation {
	return newBlankConvWithMembersType(ctx, t, tc, uid, ri, sender, tlfName,
		chat1.ConversationMembersType_KBFS)
}

func newBlankConvWithMembersType(ctx context.Context, t *testing.T, tc *kbtest.ChatTestContext,
	uid gregor1.UID, ri chat1.RemoteInterface, sender types.Sender, tlfName string,
	membersType chat1.ConversationMembersType) chat1.Conversation {
	res, err := NewConversation(ctx, tc.Context(), uid, tlfName, nil, chat1.TopicType_CHAT, membersType,
		keybase1.TLFVisibility_PRIVATE, func() chat1.RemoteInterface { return ri })
	require.NoError(t, err)
	convID := res.GetConvID()
	ires, err := ri.GetInboxRemote(ctx, chat1.GetInboxRemoteArg{
		Query: &chat1.GetInboxQuery{
			ConvID: &convID,
		},
	})
	require.NoError(t, err)
	return ires.Inbox.Full().Conversations[0]
}

func newConv(ctx context.Context, t *testing.T, tc *kbtest.ChatTestContext, uid gregor1.UID,
	ri chat1.RemoteInterface, sender types.Sender, tlfName string) chat1.Conversation {
	conv := newBlankConv(ctx, t, tc, uid, ri, sender, tlfName)
	_, _, err := sender.Send(ctx, conv.GetConvID(), chat1.MessagePlaintext{
		ClientHeader: chat1.MessageClientHeader{
			Conv:        conv.Metadata.IdTriple,
			Sender:      uid,
			TlfName:     tlfName,
			MessageType: chat1.MessageType_TEXT,
		},
		MessageBody: chat1.NewMessageBodyWithText(chat1.MessageText{Body: "foo"}),
	}, 0, nil)
	require.NoError(t, err)
	convID := conv.GetConvID()
	ires, err := ri.GetInboxRemote(ctx, chat1.GetInboxRemoteArg{
		Query: &chat1.GetInboxQuery{
			ConvID: &convID,
		},
	})
	require.NoError(t, err)
	return ires.Inbox.Full().Conversations[0]
}

func doSync(t *testing.T, syncer types.Syncer, ri chat1.RemoteInterface, uid gregor1.UID) {
	res, err := ri.SyncAll(context.TODO(), chat1.SyncAllArg{
		Uid: uid,
	})
	require.NoError(t, err)
	require.NoError(t, syncer.Sync(context.TODO(), ri, uid, &res.Chat))
}

func TestSyncerConnected(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 3)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	u1 := world.GetUsers()[1]
	u2 := world.GetUsers()[2]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true
	ibox := storage.NewInbox(tc.Context())
	store := storage.New(tc.Context(), tc.ChatG.ConvSource)

	var convs []chat1.Conversation
	convs = append(convs, newBlankConv(ctx, t, tc, uid, ri, sender, u.Username+","+u1.Username))
	convs = append(convs, newBlankConv(ctx, t, tc, uid, ri, sender, u.Username+","+u2.Username))
	convs = append(convs, newBlankConv(ctx, t, tc, uid, ri, sender, u.Username+","+u2.Username+","+u1.Username))
	for index, conv := range convs {
		t.Logf("index: %d conv: %s", index, conv.GetConvID())
	}
	// background loader will pick up all the convs from the creates above
	for i := 0; i < len(convs); i++ {
		select {
		case convID := <-list.bgConvLoads:
			require.Equal(t, convs[i].GetConvID(), convID)
		case <-time.After(20 * time.Second):
			require.Fail(t, "no background conv loaded")
		}
	}

	t.Logf("test current")
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		return chat1.NewSyncInboxResWithCurrent(), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CURRENT, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox sync received")
	}

	t.Logf("test clear")
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		return chat1.NewSyncInboxResWithClear(), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CLEAR, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox synced received")
	}
	_, _, err := ibox.ReadAll(ctx, uid)
	require.Error(t, err)
	require.IsType(t, storage.MissError{}, err)

	t.Logf("test incremental")
	mconv := convs[1]
	_, cerr := tc.ChatG.ConvSource.Pull(ctx, mconv.GetConvID(), uid, chat1.GetThreadReason_GENERAL, nil, nil)
	require.NoError(t, cerr)
	_, _, serr := tc.ChatG.InboxSource.Read(ctx, uid, types.ConversationLocalizerBlocking, true,
		nil, nil, nil)
	require.NoError(t, serr)
	_, iconvs, err := ibox.ReadAll(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, len(convs), len(iconvs))

	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		mconv.Metadata.Status = chat1.ConversationStatus_MUTED
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{mconv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_INCREMENTAL, typ)
		updates := sres.Incremental().Items
		require.Equal(t, 1, len(updates))
		require.Equal(t, convs[1].GetConvID().String(), updates[0].ConvID)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no threads stale received")
	}
	select {
	case cid := <-list.bgConvLoads:
		require.Equal(t, convs[1].GetConvID(), cid)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no background conv loaded")
	}
	vers, iconvs, err := ibox.ReadAll(context.TODO(), uid)
	require.NoError(t, err)
	require.Equal(t, len(convs), len(iconvs))
	for _, ic := range iconvs {
		if ic.GetConvID().Eq(mconv.GetConvID()) {
			require.Equal(t, chat1.ConversationStatus_MUTED, ic.Conv.Metadata.Status)
		}
	}
	require.Equal(t, chat1.ConversationStatus_UNFILED, convs[1].Metadata.Status)
	require.Equal(t, chat1.InboxVers(100), vers)
	thread, cerr := store.Fetch(context.TODO(), mconv, uid, nil, nil, nil)
	require.NoError(t, cerr)
	require.Equal(t, 1, len(thread.Thread.Messages))

	t.Logf("test server version")
	srvVers, err := ibox.ServerVersion(context.TODO(), uid)
	require.NoError(t, err)
	require.Zero(t, srvVers)
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		return chat1.NewSyncInboxResWithCurrent(), nil
	}
	ri.CacheInboxVersion = 5
	ri.CacheBodiesVersion = 5
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CLEAR, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox stale received")
	}
	_, _, err = ibox.ReadAll(ctx, uid)
	require.Error(t, err)
	require.IsType(t, storage.MissError{}, err)
	_, cerr = store.Fetch(ctx, mconv, uid, nil, nil, nil)
	require.Error(t, cerr)
	require.IsType(t, storage.MissError{}, cerr)
	_, _, serr = tc.Context().InboxSource.Read(ctx, uid, types.ConversationLocalizerBlocking, true,
		nil, nil, nil)
	require.NoError(t, serr)
	_, iconvs, err = ibox.ReadAll(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, len(convs), len(iconvs))
	srvVers, err = ibox.ServerVersion(context.TODO(), uid)
	require.NoError(t, err)
	require.Equal(t, 5, srvVers)

	// Make sure we didn't get any stales
	select {
	case <-list.threadsStale:
		require.Fail(t, "no thread stales")
	default:
	}
	select {
	case <-list.inboxStale:
		require.Fail(t, "no inbox stales")
	default:
	}
}

func TestSyncerAdHocFullReload(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 1)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true

	conv := newConv(ctx, t, tc, uid, ri, sender, u.Username)
	t.Logf("convID: %s", conv.GetConvID())
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		conv.ReaderInfo.Status = chat1.ConversationMemberStatus_LEFT
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CLEAR, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox synced received")
	}

	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		conv.Metadata.TeamType = chat1.TeamType_COMPLEX
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  101,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CLEAR, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox synced received")
	}

	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		conv.Metadata.Existence = chat1.ConversationExistence_DELETED
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  102,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CLEAR, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox synced received")
	}

	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		conv.Metadata.Existence = chat1.ConversationExistence_ABANDONED
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  103,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_CLEAR, typ)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox synced received")
	}

	// Make sure we don't get inbox stale
	select {
	case <-list.inboxStale:
		require.Fail(t, "no inbox stale")
	default:
	}
}

func TestSyncerMembersTypeChanged(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 1)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true
	uid := gregor1.UID(u.User.GetUID().ToBytes())

	conv := newConv(ctx, t, tc, uid, ri, sender, u.Username)
	t.Logf("convID: %s", conv.GetConvID())
	convID := conv.GetConvID()

	_, msg, err := sender.Send(ctx, convID, chat1.MessagePlaintext{
		ClientHeader: chat1.MessageClientHeader{
			Conv:        conv.Metadata.IdTriple,
			Sender:      uid,
			TlfName:     u.Username,
			TlfPublic:   false,
			MessageType: chat1.MessageType_TEXT,
		},
		MessageBody: chat1.NewMessageBodyWithText(chat1.MessageText{
			Body: "hi",
		}),
	}, 0, nil)
	require.NoError(t, err)
	s := storage.New(tc.Context(), tc.ChatG.ConvSource)
	storedMsgs, err := s.FetchMessages(ctx, convID, uid, []chat1.MessageID{msg.GetMessageID()})
	require.NoError(t, err)
	require.Len(t, storedMsgs, 1)
	require.NotNil(t, storedMsgs[0])
	require.Equal(t, msg.GetMessageID(), storedMsgs[0].GetMessageID())

	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		conv.Metadata.MembersType = chat1.ConversationMembersType_IMPTEAMUPGRADE
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_INCREMENTAL, typ)
		require.Equal(t, convID.String(), sres.Incremental().Items[0].ConvID)
		require.Equal(t, chat1.ConversationMembersType_IMPTEAMUPGRADE,
			sres.Incremental().Items[0].MembersType)
		storedMsgs, err = s.FetchMessages(ctx, convID, uid, []chat1.MessageID{msg.GetMessageID()})
		require.NoError(t, err)
		require.Len(t, storedMsgs, 1)
		require.Nil(t, storedMsgs[0])
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox synced received")
	}

}

func TestSyncerAppState(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 1)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true

	conv := newConv(ctx, t, tc, uid, ri, sender, u.Username)
	t.Logf("test incremental")
	tc.G.AppState.Update(keybase1.AppState_BACKGROUND)
	syncer.SendChatStaleNotifications(context.TODO(), uid, []chat1.ConversationStaleUpdate{
		chat1.ConversationStaleUpdate{
			ConvID:     conv.GetConvID(),
			UpdateType: chat1.StaleUpdateType_NEWACTIVITY,
		},
	}, true)
	select {
	case <-list.threadsStale:
		require.Fail(t, "no stale messages in bkg mode")
	default:
	}

	tc.G.AppState.Update(keybase1.AppState_FOREGROUND)
	select {
	case updates := <-list.threadsStale:
		require.Equal(t, 1, len(updates))
		require.Equal(t, chat1.StaleUpdateType_NEWACTIVITY, updates[0].UpdateType)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no stale messages")
	}

	tc.G.AppState.Update(keybase1.AppState_BACKGROUND)
	syncer.SendChatStaleNotifications(context.TODO(), uid, nil, true)
	select {
	case <-list.inboxStale:
		require.Fail(t, "no stale messages in bkg mode")
	default:
	}

	tc.G.AppState.Update(keybase1.AppState_FOREGROUND)
	select {
	case <-list.inboxStale:
	case <-time.After(20 * time.Second):
		require.Fail(t, "no inbox stale message")
	}
}

// Test that we miss an Expunge and then get it in an incremental sync,
// the messages get deleted.
func TestSyncerRetentionExpunge(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 2)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	u1 := world.GetUsers()[1]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true
	ibox := storage.NewInbox(tc.Context())
	store := storage.New(tc.Context(), tc.ChatG.ConvSource)

	tlfName := u.Username + "," + u1.Username
	mconv := newBlankConv(ctx, t, tc, uid, ri, sender, tlfName)
	select {
	case cid := <-list.bgConvLoads:
		require.Equal(t, mconv.GetConvID(), cid)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no background conv loaded")
	}

	t.Logf("test incremental")
	_, _, err := sender.Send(ctx, mconv.GetConvID(), chat1.MessagePlaintext{
		ClientHeader: chat1.MessageClientHeader{
			Conv:        mconv.Metadata.IdTriple,
			Sender:      uid,
			TlfName:     tlfName,
			MessageType: chat1.MessageType_TEXT,
		},
		MessageBody: chat1.NewMessageBodyWithText(chat1.MessageText{
			Body: "hi",
		}),
	}, 0, nil)
	require.NoError(t, err)
	tv, cerr := tc.ChatG.ConvSource.Pull(ctx, mconv.GetConvID(), uid, chat1.GetThreadReason_GENERAL, nil, nil)
	require.NoError(t, cerr)
	require.Equal(t, 2, len(tv.Messages))
	_, _, serr := tc.ChatG.InboxSource.Read(ctx, uid, types.ConversationLocalizerBlocking, true,
		nil, nil, nil)
	require.NoError(t, serr)
	select {
	case cid := <-list.bgConvLoads:
		require.Equal(t, mconv.GetConvID(), cid)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no background conv loaded")
	}
	_, iconvs, err := ibox.ReadAll(ctx, uid)
	require.NoError(t, err)
	require.Len(t, iconvs, 1)
	require.Equal(t, chat1.MessageID(2), iconvs[0].Conv.ReaderInfo.MaxMsgid)
	mconv = iconvs[0].Conv

	time.Sleep(400 * time.Millisecond)
	select {
	case <-list.bgConvLoads:
		require.Fail(t, "no loads here")
	default:
	}
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		mconv.Expunge = chat1.Expunge{Upto: 12}
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{mconv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case sres := <-list.inboxSynced:
		typ, err := sres.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_INCREMENTAL, typ)
		updates := sres.Incremental().Items
		require.Equal(t, 1, len(updates))
		require.Equal(t, mconv.GetConvID().String(), updates[0].ConvID)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no threads stale received")
	}
	select {
	case cid := <-list.bgConvLoads:
		require.Equal(t, mconv.GetConvID(), cid)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no background conv loaded")
	}
	_, iconvs, err = ibox.ReadAll(context.TODO(), uid)
	require.NoError(t, err)
	require.Len(t, iconvs, 1)
	require.Equal(t, chat1.Expunge{Upto: 12}, iconvs[0].Conv.Expunge)
	thread, cerr := store.Fetch(context.TODO(), mconv, uid, nil, nil, nil)
	require.NoError(t, cerr)
	require.True(t, len(thread.Thread.Messages) > 1)
	for i, m := range thread.Thread.Messages {
		t.Logf("message %v", i)
		require.True(t, m.IsValid())
		require.True(t, m.Valid().MessageBody.IsNil(), "remaining messages should have no body")
	}
}

func TestSyncerTeamFilter(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 2)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	u2 := world.GetUsers()[0]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true
	ibox := storage.NewInbox(tc.Context())

	iconv := newConv(ctx, t, tc, uid, ri, sender, u.Username)
	tconv := newBlankConvWithMembersType(ctx, t, tc, uid, ri, sender, u.Username+","+u2.Username,
		chat1.ConversationMembersType_TEAM)

	_, _, err := tc.ChatG.InboxSource.Read(ctx, uid, types.ConversationLocalizerBlocking, true,
		nil, nil, nil)
	require.NoError(t, err)
	_, iconvs, err := ibox.ReadAll(ctx, uid)
	require.NoError(t, err)
	require.Len(t, iconvs, 2)
	require.NoError(t, ibox.TeamTypeChanged(ctx, uid, 1, tconv.GetConvID(), chat1.TeamType_COMPLEX, nil))
	tconv.Metadata.TeamType = chat1.TeamType_COMPLEX

	t.Logf("dont sync shallow team change")
	syncConvs := []chat1.Conversation{iconv, tconv}
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: syncConvs,
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case res := <-list.inboxSynced:
		typ, err := res.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_INCREMENTAL, typ)
		require.Equal(t, 1, len(res.Incremental().Items))
		require.Equal(t, iconv.GetConvID().String(), res.Incremental().Items[0].ConvID)
	case <-time.After(20 * time.Second):
		require.Fail(t, "no sync")
	}

	t.Logf("sync it if metadata changed")
	for index, msg := range tconv.MaxMsgSummaries {
		if msg.GetMessageType() == chat1.MessageType_METADATA {
			tconv.MaxMsgSummaries[index] = chat1.MessageSummary{
				MsgID:       10,
				MessageType: chat1.MessageType_METADATA,
			}
		}
	}
	syncConvs = []chat1.Conversation{iconv, tconv}
	doSync(t, syncer, ri, uid)
	select {
	case res := <-list.inboxSynced:
		typ, err := res.SyncType()
		require.NoError(t, err)
		require.Equal(t, chat1.SyncInboxResType_INCREMENTAL, typ)
		require.Equal(t, 2, len(res.Incremental().Items))
	case <-time.After(20 * time.Second):
		require.Fail(t, "no sync")
	}
}

func TestSyncerBackgroundLoader(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 2)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true
	hcs := tc.Context().ConvSource.(*HybridConversationSource)
	if hcs == nil {
		t.Skip()
	}

	conv := newBlankConv(ctx, t, tc, uid, ri, sender, u.Username)
	select {
	case <-list.bgConvLoads:
	case <-time.After(20 * time.Second):
		require.Fail(t, "no conv load on sync")
	}
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case <-list.bgConvLoads:
	case <-time.After(20 * time.Second):
		require.Fail(t, "no conv load on sync")
	}
	time.Sleep(400 * time.Millisecond)
	select {
	case <-list.bgConvLoads:
		require.Fail(t, "no conv load here")
	default:
	}

	_, txtMsg, err := sender.Send(ctx, conv.GetConvID(), chat1.MessagePlaintext{
		ClientHeader: chat1.MessageClientHeader{
			Conv:        conv.Metadata.IdTriple,
			Sender:      u.User.GetUID().ToBytes(),
			TlfName:     u.Username,
			MessageType: chat1.MessageType_TEXT,
		},
		MessageBody: chat1.NewMessageBodyWithText(chat1.MessageText{
			Body: "MIKE!!!!",
		}),
	}, 0, nil)
	require.NoError(t, err)
	_, delMsg, err := sender.Send(ctx, conv.GetConvID(), chat1.MessagePlaintext{
		ClientHeader: chat1.MessageClientHeader{
			Conv:        conv.Metadata.IdTriple,
			Sender:      u.User.GetUID().ToBytes(),
			TlfName:     u.Username,
			MessageType: chat1.MessageType_DELETE,
			Supersedes:  txtMsg.GetMessageID(),
		},
		MessageBody: chat1.NewMessageBodyWithDelete(chat1.MessageDelete{
			MessageIDs: []chat1.MessageID{txtMsg.GetMessageID()},
		}),
	}, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, delMsg)
	require.NoError(t, hcs.storage.ClearAll(context.TODO(), conv.GetConvID(), uid))
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		conv.MaxMsgs = append(conv.MaxMsgs, *delMsg)
		conv.MaxMsgSummaries = append(conv.MaxMsgSummaries, delMsg.Summary())
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  200,
			Convs: []chat1.Conversation{conv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	select {
	case <-list.bgConvLoads:
	case <-time.After(2 * time.Second):
		require.Fail(t, "no conv load on sync")
	}
	time.Sleep(400 * time.Millisecond)
	select {
	case <-list.bgConvLoads:
		require.Fail(t, "no conv load here")
	default:
	}
}

func TestSyncerBackgroundLoaderRemoved(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 2)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true

	conv := newBlankConv(ctx, t, tc, uid, ri, sender, u.Username)
	select {
	case <-list.bgConvLoads:
	case <-time.After(20 * time.Second):
		require.Fail(t, "no conv load on sync")
	}
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		sconv := conv.DeepCopy()
		sconv.ReaderInfo.Status = chat1.ConversationMemberStatus_REMOVED
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{sconv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	time.Sleep(400 * time.Millisecond)
	select {
	case <-list.bgConvLoads:
		require.Fail(t, "no sync should happen")
	default:
	}
	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		sconv := conv.DeepCopy()
		sconv.Metadata.Existence = chat1.ConversationExistence_ARCHIVED
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{sconv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	time.Sleep(400 * time.Millisecond)
	select {
	case <-list.bgConvLoads:
		require.Fail(t, "no sync should happen")
	default:
	}
}

func TestSyncerStorageClear(t *testing.T) {
	ctx, world, ri2, _, sender, list := setupTest(t, 2)
	defer world.Cleanup()

	ri := ri2.(*kbtest.ChatRemoteMock)
	u := world.GetUsers()[0]
	uid := u.User.GetUID().ToBytes()
	tc := world.Tcs[u.Username]
	syncer := NewSyncer(tc.Context())
	syncer.isConnected = true

	conv := newBlankConv(ctx, t, tc, uid, ri, sender, u.Username)
	select {
	case <-list.bgConvLoads:
	case <-time.After(20 * time.Second):
		require.Fail(t, "no conv load on sync")
	}
	tv, err := tc.Context().ConvSource.PullLocalOnly(ctx, conv.GetConvID(), uid, nil, nil, 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(tv.Messages))

	ri.SyncInboxFunc = func(m *kbtest.ChatRemoteMock, ctx context.Context, vers chat1.InboxVers) (chat1.SyncInboxRes, error) {
		sconv := conv.DeepCopy()
		sconv.ReaderInfo.Status = chat1.ConversationMemberStatus_REMOVED
		return chat1.NewSyncInboxResWithIncremental(chat1.SyncIncrementalRes{
			Vers:  100,
			Convs: []chat1.Conversation{sconv},
		}), nil
	}
	doSync(t, syncer, ri, uid)
	time.Sleep(400 * time.Millisecond)

	_, err = tc.Context().ConvSource.PullLocalOnly(ctx, conv.GetConvID(), uid, nil, nil, 0)
	require.Error(t, err)
	require.IsType(t, storage.MissError{}, err)
}
