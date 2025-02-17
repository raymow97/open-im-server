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

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/OpenIMSDK/protocol/constant"
	"github.com/OpenIMSDK/tools/apiresp"
	"github.com/OpenIMSDK/tools/discoveryregistry"
	"github.com/OpenIMSDK/tools/errs"
	"github.com/OpenIMSDK/tools/log"
	"github.com/OpenIMSDK/tools/mw"
	"github.com/OpenIMSDK/tools/tokenverify"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/openimsdk/open-im-server/v3/pkg/authverify"
	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/pkg/common/db/cache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/db/controller"
	kdisc "github.com/openimsdk/open-im-server/v3/pkg/common/discoveryregister"
	ginprom "github.com/openimsdk/open-im-server/v3/pkg/common/ginprometheus"
	"github.com/openimsdk/open-im-server/v3/pkg/common/prommetrics"
	"github.com/openimsdk/open-im-server/v3/pkg/rpcclient"
	util "github.com/openimsdk/open-im-server/v3/pkg/util/genutil"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Start(config *config.GlobalConfig, port int, proPort int) error {
	log.ZDebug(context.Background(), "configAPI1111111111111111111", config, "port", port, "javafdasfs")
	if port == 0 || proPort == 0 {
		err := "port or proPort is empty:" + strconv.Itoa(port) + "," + strconv.Itoa(proPort)
		return errs.Wrap(fmt.Errorf(err))
	}
	rdb, err := cache.NewRedis(config)
	if err != nil {
		return err
	}

	var client discoveryregistry.SvcDiscoveryRegistry

	// Determine whether zk is passed according to whether it is a clustered deployment
	client, err = kdisc.NewDiscoveryRegister(config)
	if err != nil {
		return errs.Wrap(err, "register discovery err")
	}

	if err = client.CreateRpcRootNodes(config.GetServiceNames()); err != nil {
		return errs.Wrap(err, "create rpc root nodes error")
	}

	if err = client.RegisterConf2Registry(constant.OpenIMCommonConfigKey, config.EncodeConfig()); err != nil {
		return errs.Wrap(err)
	}
	var (
		netDone = make(chan struct{}, 1)
		netErr  error
	)
	router := newGinRouter(client, rdb, config)
	if config.Prometheus.Enable {
		go func() {
			p := ginprom.NewPrometheus("app", prommetrics.GetGinCusMetrics("Api"))
			p.SetListenAddress(fmt.Sprintf(":%d", proPort))
			if err = p.Use(router); err != nil && err != http.ErrServerClosed {
				netErr = errs.Wrap(err, fmt.Sprintf("prometheus start err: %d", proPort))
				netDone <- struct{}{}
			}
		}()

	}

	var address string
	if config.Api.ListenIP != "" {
		address = net.JoinHostPort(config.Api.ListenIP, strconv.Itoa(port))
	} else {
		address = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	}

	server := http.Server{Addr: address, Handler: router}

	go func() {
		err = server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			netErr = errs.Wrap(err, fmt.Sprintf("api start err: %s", server.Addr))
			netDone <- struct{}{}

		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	select {
	case <-sigs:
		util.SIGTERMExit()
		err := server.Shutdown(ctx)
		if err != nil {
			return errs.Wrap(err, "shutdown err")
		}
	case <-netDone:
		close(netDone)
		return netErr
	}
	return nil
}

func newGinRouter(disCov discoveryregistry.SvcDiscoveryRegistry, rdb redis.UniversalClient, config *config.GlobalConfig) *gin.Engine {
	disCov.AddOption(mw.GrpcClient(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultServiceConfig(fmt.Sprintf(`{"LoadBalancingPolicy": "%s"}`, "round_robin")))
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
		_ = v.RegisterValidation("required_if", RequiredIf)
	}
	r.Use(gin.Recovery(), mw.CorsHandler(), mw.GinParseOperationID())
	// init rpc client here
	userRpc := rpcclient.NewUser(disCov, config)
	groupRpc := rpcclient.NewGroup(disCov, config)
	friendRpc := rpcclient.NewFriend(disCov, config)
	messageRpc := rpcclient.NewMessage(disCov, config)
	conversationRpc := rpcclient.NewConversation(disCov, config)
	authRpc := rpcclient.NewAuth(disCov, config)
	thirdRpc := rpcclient.NewThird(disCov, config)

	u := NewUserApi(*userRpc)
	m := NewMessageApi(messageRpc, userRpc)
	ParseToken := GinParseToken(rdb, config)
	userRouterGroup := r.Group("/user")
	{
		userRouterGroup.POST("/user_register", u.UserRegister)
		userRouterGroup.POST("/update_user_info", ParseToken, u.UpdateUserInfo)
		userRouterGroup.POST("/update_user_info_ex", ParseToken, u.UpdateUserInfoEx)
		userRouterGroup.POST("/set_global_msg_recv_opt", ParseToken, u.SetGlobalRecvMessageOpt)
		userRouterGroup.POST("/get_users_info", ParseToken, u.GetUsersPublicInfo)
		userRouterGroup.POST("/get_all_users_uid", ParseToken, u.GetAllUsersID)
		userRouterGroup.POST("/account_check", ParseToken, u.AccountCheck)
		userRouterGroup.POST("/get_users", ParseToken, u.GetUsers)
		userRouterGroup.POST("/get_users_online_status", ParseToken, u.GetUsersOnlineStatus)
		userRouterGroup.POST("/get_users_online_token_detail", ParseToken, u.GetUsersOnlineTokenDetail)
		userRouterGroup.POST("/subscribe_users_status", ParseToken, u.SubscriberStatus)
		userRouterGroup.POST("/get_users_status", ParseToken, u.GetUserStatus)
		userRouterGroup.POST("/get_subscribe_users_status", ParseToken, u.GetSubscribeUsersStatus)

		userRouterGroup.POST("/process_user_command_add", ParseToken, u.ProcessUserCommandAdd)
		userRouterGroup.POST("/process_user_command_delete", ParseToken, u.ProcessUserCommandDelete)
		userRouterGroup.POST("/process_user_command_update", ParseToken, u.ProcessUserCommandUpdate)
		userRouterGroup.POST("/process_user_command_get", ParseToken, u.ProcessUserCommandGet)
		userRouterGroup.POST("/process_user_command_get_all", ParseToken, u.ProcessUserCommandGetAll)

		userRouterGroup.POST("/add_notification_account", ParseToken, u.AddNotificationAccount)
		userRouterGroup.POST("/update_notification_account", ParseToken, u.UpdateNotificationAccountInfo)
		userRouterGroup.POST("/search_notification_account", ParseToken, u.SearchNotificationAccount)
	}
	// friend routing group
	friendRouterGroup := r.Group("/friend", ParseToken)
	{
		f := NewFriendApi(*friendRpc)
		friendRouterGroup.POST("/delete_friend", f.DeleteFriend)
		friendRouterGroup.POST("/get_friend_apply_list", f.GetFriendApplyList)
		friendRouterGroup.POST("/get_designated_friend_apply", f.GetDesignatedFriendsApply)
		friendRouterGroup.POST("/get_self_friend_apply_list", f.GetSelfApplyList)
		friendRouterGroup.POST("/get_friend_list", f.GetFriendList)
		friendRouterGroup.POST("/get_designated_friends", f.GetDesignatedFriends)
		friendRouterGroup.POST("/add_friend", f.ApplyToAddFriend)
		friendRouterGroup.POST("/add_friend_response", f.RespondFriendApply)
		friendRouterGroup.POST("/set_friend_remark", f.SetFriendRemark)
		friendRouterGroup.POST("/add_black", f.AddBlack)
		friendRouterGroup.POST("/get_black_list", f.GetPaginationBlacks)
		friendRouterGroup.POST("/remove_black", f.RemoveBlack)
		friendRouterGroup.POST("/import_friend", f.ImportFriends)
		friendRouterGroup.POST("/is_friend", f.IsFriend)
		friendRouterGroup.POST("/get_friend_id", f.GetFriendIDs)
		friendRouterGroup.POST("/get_specified_friends_info", f.GetSpecifiedFriendsInfo)
		friendRouterGroup.POST("/update_friends", f.UpdateFriends)
	}
	g := NewGroupApi(*groupRpc)
	groupRouterGroup := r.Group("/group", ParseToken)
	{
		groupRouterGroup.POST("/create_group", g.CreateGroup)
		groupRouterGroup.POST("/set_group_info", g.SetGroupInfo)
		groupRouterGroup.POST("/join_group", g.JoinGroup)
		groupRouterGroup.POST("/quit_group", g.QuitGroup)
		groupRouterGroup.POST("/group_application_response", g.ApplicationGroupResponse)
		groupRouterGroup.POST("/transfer_group", g.TransferGroupOwner)
		groupRouterGroup.POST("/get_recv_group_applicationList", g.GetRecvGroupApplicationList)
		groupRouterGroup.POST("/get_user_req_group_applicationList", g.GetUserReqGroupApplicationList)
		groupRouterGroup.POST("/get_group_users_req_application_list", g.GetGroupUsersReqApplicationList)
		groupRouterGroup.POST("/get_groups_info", g.GetGroupsInfo)
		groupRouterGroup.POST("/kick_group", g.KickGroupMember)
		groupRouterGroup.POST("/get_group_members_info", g.GetGroupMembersInfo)
		groupRouterGroup.POST("/get_group_member_list", g.GetGroupMemberList)
		groupRouterGroup.POST("/invite_user_to_group", g.InviteUserToGroup)
		groupRouterGroup.POST("/get_joined_group_list", g.GetJoinedGroupList)
		groupRouterGroup.POST("/dismiss_group", g.DismissGroup) //
		groupRouterGroup.POST("/mute_group_member", g.MuteGroupMember)
		groupRouterGroup.POST("/cancel_mute_group_member", g.CancelMuteGroupMember)
		groupRouterGroup.POST("/mute_group", g.MuteGroup)
		groupRouterGroup.POST("/cancel_mute_group", g.CancelMuteGroup)
		groupRouterGroup.POST("/set_group_member_info", g.SetGroupMemberInfo)
		groupRouterGroup.POST("/get_group_abstract_info", g.GetGroupAbstractInfo)
		groupRouterGroup.POST("/get_groups", g.GetGroups)
		groupRouterGroup.POST("/get_group_member_user_id", g.GetGroupMemberUserIDs)
	}
	superGroupRouterGroup := r.Group("/super_group", ParseToken)
	{
		superGroupRouterGroup.POST("/get_joined_group_list", g.GetJoinedSuperGroupList)
		superGroupRouterGroup.POST("/get_groups_info", g.GetSuperGroupsInfo)
	}
	// certificate
	authRouterGroup := r.Group("/auth")
	{
		a := NewAuthApi(*authRpc)
		authRouterGroup.POST("/user_token", a.UserToken)
		authRouterGroup.POST("/get_user_token", ParseToken, a.GetUserToken)
		authRouterGroup.POST("/parse_token", a.ParseToken)
		authRouterGroup.POST("/force_logout", ParseToken, a.ForceLogout)
	}
	// Third service
	thirdGroup := r.Group("/third", ParseToken)
	{
		t := NewThirdApi(*thirdRpc)
		thirdGroup.GET("/prometheus", t.GetPrometheus)
		thirdGroup.POST("/fcm_update_token", t.FcmUpdateToken)
		thirdGroup.POST("/set_app_badge", t.SetAppBadge)

		logs := thirdGroup.Group("/logs")
		logs.POST("/upload", t.UploadLogs)
		logs.POST("/delete", t.DeleteLogs)
		logs.POST("/search", t.SearchLogs)

		objectGroup := r.Group("/object", ParseToken)

		objectGroup.POST("/part_limit", t.PartLimit)
		objectGroup.POST("/part_size", t.PartSize)
		objectGroup.POST("/initiate_multipart_upload", t.InitiateMultipartUpload)
		objectGroup.POST("/auth_sign", t.AuthSign)
		objectGroup.POST("/complete_multipart_upload", t.CompleteMultipartUpload)
		objectGroup.POST("/access_url", t.AccessURL)
		objectGroup.POST("/initiate_form_data", t.InitiateFormData)
		objectGroup.POST("/complete_form_data", t.CompleteFormData)
		objectGroup.GET("/*name", t.ObjectRedirect)
	}
	// Message
	msgGroup := r.Group("/msg", ParseToken)
	{
		msgGroup.POST("/newest_seq", m.GetSeq)
		msgGroup.POST("/search_msg", m.SearchMsg)
		msgGroup.POST("/send_msg", m.SendMessage)
		msgGroup.POST("/send_business_notification", m.SendBusinessNotification)
		msgGroup.POST("/pull_msg_by_seq", m.PullMsgBySeqs)
		msgGroup.POST("/revoke_msg", m.RevokeMsg)
		msgGroup.POST("/mark_msgs_as_read", m.MarkMsgsAsRead)
		msgGroup.POST("/mark_conversation_as_read", m.MarkConversationAsRead)
		msgGroup.POST("/get_conversations_has_read_and_max_seq", m.GetConversationsHasReadAndMaxSeq)
		msgGroup.POST("/set_conversation_has_read_seq", m.SetConversationHasReadSeq)

		msgGroup.POST("/clear_conversation_msg", m.ClearConversationsMsg)
		msgGroup.POST("/user_clear_all_msg", m.UserClearAllMsg)
		msgGroup.POST("/delete_msgs", m.DeleteMsgs)
		msgGroup.POST("/delete_msg_phsical_by_seq", m.DeleteMsgPhysicalBySeq)
		msgGroup.POST("/delete_msg_physical", m.DeleteMsgPhysical)

		msgGroup.POST("/batch_send_msg", m.BatchSendMsg)
		msgGroup.POST("/check_msg_is_send_success", m.CheckMsgIsSendSuccess)
		msgGroup.POST("/get_server_time", m.GetServerTime)
	}
	// Conversation
	conversationGroup := r.Group("/conversation", ParseToken)
	{
		c := NewConversationApi(*conversationRpc)
		conversationGroup.POST("/get_sorted_conversation_list", c.GetSortedConversationList)
		conversationGroup.POST("/get_all_conversations", c.GetAllConversations)
		conversationGroup.POST("/get_conversation", c.GetConversation)
		conversationGroup.POST("/get_conversations", c.GetConversations)
		conversationGroup.POST("/set_conversations", c.SetConversations)
		conversationGroup.POST("/get_conversation_offline_push_user_ids", c.GetConversationOfflinePushUserIDs)
	}

	statisticsGroup := r.Group("/statistics", ParseToken)
	{
		statisticsGroup.POST("/user/register", u.UserRegisterCount)
		statisticsGroup.POST("/user/active", m.GetActiveUser)
		statisticsGroup.POST("/group/create", g.GroupCreateCount)
		statisticsGroup.POST("/group/active", m.GetActiveGroup)
	}
	return r
}

func GinParseToken(rdb redis.UniversalClient, config *config.GlobalConfig) gin.HandlerFunc {
	dataBase := controller.NewAuthDatabase(
		cache.NewMsgCacheModel(rdb, config),
		config.Secret,
		config.TokenPolicy.Expire,
		config,
	)
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodPost:
			token := c.Request.Header.Get(constant.Token)
			if token == "" {
				log.ZWarn(c, "header get token error", errs.ErrArgs.Wrap("header must have token"))
				apiresp.GinError(c, errs.ErrArgs.Wrap("header must have token"))
				c.Abort()
				return
			}
			claims, err := tokenverify.GetClaimFromToken(token, authverify.Secret(config.Secret))
			if err != nil {
				log.ZWarn(c, "jwt get token error", errs.ErrTokenUnknown.Wrap())
				apiresp.GinError(c, errs.ErrTokenUnknown.Wrap())
				c.Abort()
				return
			}
			m, err := dataBase.GetTokensWithoutError(c, claims.UserID, claims.PlatformID)
			if err != nil {
				apiresp.GinError(c, errs.ErrTokenNotExist.Wrap())
				c.Abort()
				return
			}
			if len(m) == 0 {
				apiresp.GinError(c, errs.ErrTokenNotExist.Wrap())
				c.Abort()
				return
			}
			if v, ok := m[token]; ok {
				switch v {
				case constant.NormalToken:
				case constant.KickedToken:
					apiresp.GinError(c, errs.ErrTokenKicked.Wrap())
					c.Abort()
					return
				default:
					apiresp.GinError(c, errs.ErrTokenUnknown.Wrap())
					c.Abort()
					return
				}
			} else {
				apiresp.GinError(c, errs.ErrTokenNotExist.Wrap())
				c.Abort()
				return
			}
			c.Set(constant.OpUserPlatform, constant.PlatformIDToName(claims.PlatformID))
			c.Set(constant.OpUserID, claims.UserID)
			c.Next()
		}
	}
}

// // handleGinError logs and returns an error response through Gin context.
// func handleGinError(c *gin.Context, logMessage string, errType errs.CodeError, detail string) {
// 	wrappedErr := errType.Wrap(detail)
// 	apiresp.GinError(c, wrappedErr)
// 	c.Abort()
// }
