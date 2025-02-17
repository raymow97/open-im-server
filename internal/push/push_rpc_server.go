// Copyright © 2023 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package push

import (
	"context"

	"github.com/OpenIMSDK/protocol/constant"
	pbpush "github.com/OpenIMSDK/protocol/push"
	"github.com/OpenIMSDK/tools/discoveryregistry"
	"github.com/OpenIMSDK/tools/log"
	"github.com/OpenIMSDK/tools/utils"
	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/pkg/common/db/cache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/db/controller"
	"github.com/openimsdk/open-im-server/v3/pkg/rpccache"
	"github.com/openimsdk/open-im-server/v3/pkg/rpcclient"
	"google.golang.org/grpc"
)

type pushServer struct {
	pusher *Pusher
	config *config.GlobalConfig
}

func Start(config *config.GlobalConfig, client discoveryregistry.SvcDiscoveryRegistry, server *grpc.Server) error {
	rdb, err := cache.NewRedis(config)
	if err != nil {
		return err
	}
	cacheModel := cache.NewMsgCacheModel(rdb, config)
	offlinePusher := NewOfflinePusher(config, cacheModel)
	database := controller.NewPushDatabase(cacheModel)
	groupRpcClient := rpcclient.NewGroupRpcClient(client, config)
	conversationRpcClient := rpcclient.NewConversationRpcClient(client, config)
	msgRpcClient := rpcclient.NewMessageRpcClient(client, config)
	pusher := NewPusher(
		config,
		client,
		offlinePusher,
		database,
		rpccache.NewGroupLocalCache(groupRpcClient, rdb),
		rpccache.NewConversationLocalCache(conversationRpcClient, rdb),
		&conversationRpcClient,
		&groupRpcClient,
		&msgRpcClient,
	)

	pbpush.RegisterPushMsgServiceServer(server, &pushServer{
		pusher: pusher,
		config: config,
	})

	consumer, err := NewConsumer(config, pusher)
	if err != nil {
		return err
	}

	consumer.Start()

	return nil
}

func (r *pushServer) PushMsg(ctx context.Context, pbData *pbpush.PushMsgReq) (resp *pbpush.PushMsgResp, err error) {
	switch pbData.MsgData.SessionType {
	case constant.SuperGroupChatType:
		err = r.pusher.Push2SuperGroup(ctx, pbData.MsgData.GroupID, pbData.MsgData)
	default:
		var pushUserIDList []string
		isSenderSync := utils.GetSwitchFromOptions(pbData.MsgData.Options, constant.IsSenderSync)
		if !isSenderSync {
			pushUserIDList = append(pushUserIDList, pbData.MsgData.RecvID)
		} else {
			pushUserIDList = append(pushUserIDList, pbData.MsgData.RecvID, pbData.MsgData.SendID)
		}
		err = r.pusher.Push2User(ctx, pushUserIDList, pbData.MsgData)
	}
	if err != nil {
		if err != errNoOfflinePusher {
			return nil, err
		} else {
			log.ZWarn(ctx, "offline push failed", err, "msg", pbData.String())
		}
	}
	return &pbpush.PushMsgResp{}, nil
}

func (r *pushServer) DelUserPushToken(
	ctx context.Context,
	req *pbpush.DelUserPushTokenReq,
) (resp *pbpush.DelUserPushTokenResp, err error) {
	if err = r.pusher.database.DelFcmToken(ctx, req.UserID, int(req.PlatformID)); err != nil {
		return nil, err
	}
	return &pbpush.DelUserPushTokenResp{}, nil
}
