package main

import (
	"fmt"

	"github.com/TruStory/truchain/x/slashing"

	"github.com/TruStory/octopus/services/truapi/db"

	"github.com/TruStory/truchain/x/staking"
	"github.com/tendermint/tendermint/types"
)

func (s *service) processArgumentCreated(data []byte, notifications chan<- *Notification) {
	argument := staking.Argument{}
	err := staking.ModuleCodec.UnmarshalJSON(data, &argument)
	if err != nil {
		s.log.WithError(err).Error("error decoding argument created event")
		return
	}
	claimParticipants, err := s.getClaimParticipantsByArgumentId(int64(argument.ID))
	if err != nil {
		s.log.WithError(err).Error("error getting participants ")
		return
	}
	meta := db.NotificationMeta{
		ClaimID:    &claimParticipants.ClaimID,
		ArgumentID: uint64Ptr(argument.ID),
	}

	creatorAddress := argument.Creator.String()
	notified := make(map[string]bool)

	// check mentions first
	_, addresses := s.parseCosmosMentions(argument.Body)
	mentionType := db.MentionArgument
	addresses = unique(addresses)
	for _, address := range addresses {
		notified[address] = true
		notifications <- &Notification{
			From:   &creatorAddress,
			To:     address,
			Msg:    fmt.Sprintf("mentioned you %s: %s", mentionType.String(), argument.Summary),
			TypeID: int64(argument.ID),
			Type:   db.NotificationMentionAction,
			Meta: db.NotificationMeta{
				ClaimID:     &claimParticipants.ClaimID,
				ArgumentID:  uint64Ptr(argument.ID),
				MentionType: &mentionType,
			},
			Action: "Mentioned you in an argument",
			Trim:   true,
		}
	}

	if _, ok := notified[creatorAddress]; creatorAddress != claimParticipants.Creator && !ok {
		notified[creatorAddress] = true
		notifications <- &Notification{
			From:   strPtr(argument.Creator.String()),
			To:     claimParticipants.Creator,
			Msg:    fmt.Sprintf("added a new argument on a claim you created: %s", argument.Summary),
			TypeID: int64(argument.ID),
			Type:   db.NotificationNewArgument,
			Meta:   meta,
			Action: "New Argument",
		}
	}

	for _, p := range claimParticipants.Participants {
		if _, ok := notified[p]; ok {
			continue
		}
		notified[p] = true
		notifications <- &Notification{
			From:   strPtr(argument.Creator.String()),
			To:     p,
			Msg:    fmt.Sprintf("added a new argument on a claim you participated in: %s", argument.Summary),
			TypeID: int64(argument.ID),
			Type:   db.NotificationNewArgument,
			Meta:   meta,
			Action: "New Argument",
		}
	}
}

func (s *service) processUpvote(data []byte, notifications chan<- *Notification) {
	stake := staking.Stake{}
	err := staking.ModuleCodec.UnmarshalJSON(data, &stake)
	if err != nil {
		s.log.WithError(err).Error("error decoding argument created event")
		return
	}
	argument, err := s.getArgumentSummary(int64(stake.ArgumentID))
	if err != nil {
		s.log.WithError(err).Error("error getting participants ")
		return
	}
	meta := db.NotificationMeta{
		ClaimID:    &argument.ClaimArgument.ClaimID,
		ArgumentID: uint64Ptr(stake.ArgumentID),
	}

	argumentCreatorAddress := argument.ClaimArgument.Creator.Address
	notifications <- &Notification{
		From:   strPtr(stake.Creator.String()),
		To:     argumentCreatorAddress,
		Msg:    fmt.Sprintf("agreed with your argument: %s", argument.ClaimArgument.Summary),
		TypeID: int64(stake.ArgumentID),
		Type:   db.NotificationAgreeReceived,
		Meta:   meta,
		Action: "Agree Received",
	}
}

//func getTagValue(key string, tags sdk.Tags) ([]byte, bool) {
//	for _, tag := range tags.ToKVPairs() {
//		if string(tag.Key) == key {
//			return tag.Value, true
//		}
//	}
//	return nil, false
//}

func (s *service) notifySlashes(punishResults []slashing.PunishmentResult,
	notifications chan<- *Notification, meta db.NotificationMeta, argumentID int64, minCount string) {
	slashed := make(map[string]bool)
	for _, p := range punishResults {
		if p.Type == slashing.PunishmentCuratorRewarded {
			continue
		}
		slashed[p.AppAccAddress.String()] = true
	}

	for k := range slashed {
		notifications <- &Notification{
			To: k,
			Msg: fmt.Sprintf("You've been penalized! You've either wrote an argument that has been marked Not Helpful %s times or Agreed with an argument marked as Not Helpful %s times.",
				minCount, minCount),
			TypeID: argumentID,
			Type:   db.NotificationSlashed,
			Meta:   meta,
			Action: "Slashed",
		}
	}

	for _, p := range punishResults {

		if p.Type == slashing.PunishmentCuratorRewarded {
			notifications <- &Notification{
				To: p.AppAccAddress.String(),
				Msg: fmt.Sprintf("You just earned %s %s from an argument you marked as Not Helpful",
					humanReadable(p.Coin), db.CoinDisplayName),
				TypeID: argumentID,
				Type:   db.NotificationEarnedStake,
				Meta:   meta,
				Action: fmt.Sprintf("Earned %s", db.CoinDisplayName),
			}
		}
		if p.Type == slashing.PunishmentJailed {
			notifications <- &Notification{
				To:     p.AppAccAddress.String(),
				Msg:    "You've been slashed too many times and sent to jail. Basic privileges will be stripped.",
				TypeID: argumentID,
				Type:   db.NotificationJailed,
				Meta:   meta,
				Action: "Jailed",
			}
		}
	}
}

//func (s *service) processSlash(data []byte, tags sdk.Tags, notifications chan<- *Notification) {
//	slash := slashing.Slash{}
//	err := slashing.ModuleCodec.UnmarshalJSON(data, &slash)
//	if err != nil {
//		s.log.WithError(err).Error("error decoding argument created event")
//		return
//	}
//	argument, err := s.getArgumentSummary(int64(slash.ArgumentID))
//	if err != nil {
//		s.log.WithError(err).Error("error getting participants ")
//		return
//	}
//	meta := db.NotificationMeta{
//		ClaimID:    &argument.ClaimArgument.ClaimID,
//		ArgumentID: uint64Ptr(slash.ArgumentID),
//	}
//
//	reason := slash.Reason.String()
//	if slash.Reason == slashing.SlashReasonOther {
//		reason = slash.DetailedReason
//	}
//	notifications <- &Notification{
//		To:     argument.ClaimArgument.Creator.Address,
//		Msg:    fmt.Sprintf("Someone marked your argument as **Not Helpful** because: **%s** ", reason),
//		TypeID: int64(slash.ArgumentID),
//		Type:   db.NotificationNotHelpful,
//		Meta:   meta,
//		Action: "Not Helpful received on an Argument",
//	}
//
//	b, ok := getTagValue(slashingtags.SlashResults, tags)
//	minSlashCount, _ := getTagValue("min-slash-count", tags)
//	count := string(minSlashCount)
//	if ok {
//		punishResults := make([]slashing.PunishmentResult, 0)
//		err := json.Unmarshal(b, &punishResults)
//		if err != nil {
//			s.log.WithError(err).Warn("error decoding punish results")
//		}
//
//		if err == nil {
//			s.notifySlashes(punishResults, notifications, meta, int64(slash.ArgumentID), count)
//		}
//	}
//}

func (s *service) processTxEvent(evt types.EventDataTx, notifications chan<- *Notification) {
	//for _, tag := range evt.Result.Tags {
	//	action := string(tag.Value)
	//	switch action {
	//	case "create-argument":
	//		s.processArgumentCreated(evt.Result.Data, notifications)
	//	case "create-upvote":
	//		s.processUpvote(evt.Result.Data, notifications)
	//	case "create-slash":
	//		s.processSlash(evt.Result.Data, evt.Result.Tags, notifications)
	//	}
	//}
	fmt.Println("in processTxEvent")
}
