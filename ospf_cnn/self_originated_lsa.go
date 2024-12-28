package ospf_cnn

import (
	packet2 "github.com/SvenShi/ospf-neighbor/ospf_cnn/packet"
	"time"

	"github.com/gopacket/gopacket/layers"
)

func (i *Interface) newRouterLSA() packet2.LSAdvertisement {
	routerLSA := packet2.LSAdvertisement{
		LSAheader: packet2.LSAheader{
			LSType: layers.RouterLSAtypeV2,
			// LS Type   Link State ID
			// _______________________________________________
			// 1         The originating router's Router ID.
			// 2         The IP interface address of the network's Designated Router.
			// 3         The destination network's IP address.
			// 4         The Router ID of the described AS boundary router.
			// 5         The destination network's IP address.
			LinkStateID: i.Area.ins.RouterId,
			AdvRouter:   i.Area.ins.RouterId,
			LSSeqNumber: packet2.InitialSequenceNumber,
			LSOptions: func() uint8 {
				ret := packet2.BitOption(0)
				if i.Area.ExternalRoutingCapability {
					ret = ret.SetBit(packet2.CapabilityEbit)
				}
				return uint8(ret)
			}(),
		},
		Content: packet2.V2RouterLSA{
			RouterLSAV2: layers.RouterLSAV2{
				Flags: func() uint8 {
					ret := packet2.BitOption(0)
					if i.Area.ins.ASBR {
						ret = ret.SetBit(packet2.RouterLSAFlagEbit)
					}
					return uint8(ret)
				}(),
				Links: 1,
			},
			Routers: []packet2.RouterV2{
				{
					RouterV2: layers.RouterV2{
						// Type   Description
						// __________________________________________________
						// 1      Point-to-point connection to another router
						// 2      Connection to a transit network
						// 3      Connection to a stub network
						// 4      Virtual link
						Type: 2,
						// Type   Link ID
						// ______________________________________
						// 1      Neighboring router's Router ID
						// 2      IP address of Designated Router
						// 3      IP network/subnet number
						// 4      Neighboring router's Router ID
						LinkID: i.DR.Load(),
						//连接数据，其值取决于连接的类型：
						//unnumbered P2P：接口的索引值。
						//Stub网络：子网掩码。
						//其他连接：设备接口的IP地址。
						LinkData: ipv4BytesToUint32(i.Address.IP.To4()),
						Metric:   10,
					},
				},
			},
		},
	}
	return routerLSA
}

func (a *Area) tryUpdatingExistingLSA(id packet2.LSAIdentity, i *Interface, modFn func(lsa *packet2.LSAdvertisement)) (exist bool) {
	_, lsa, _, ok := a.lsDbGetLSAByIdentity(id, true)
	if ok {
		modFn(&lsa)
		// update LSA header for re-originating
		seqIncred := lsa.PrepareReOriginating(true)
		if err := lsa.FixLengthAndChkSum(); err != nil {
			if i != nil {
				LogErr("area %v err fix chkSum while updating LSA(%+v) with interface %v", a.AreaId, id, i.c.ifi.Name)
			} else {
				LogErr("area %v err fix chkSum while updating LSA(%+v)", a.AreaId, id)
			}
			return true
		}
		// Check if the existing LSA exceed maxSeqNum.
		// This is done by checking seqIncrement failed and LSSeq >= MaxSequenceNumber
		if !seqIncred && int32(lsa.LSSeqNumber) >= packet2.MaxSequenceNumber {
			a.doLSASeqNumWrappingAndFloodNewLSA(lsa.GetLSAIdentity(), lsa)
			return true
		}
		if a.lsDbInstallNewLSA(lsa) {
			if i != nil {
				LogDebug("area %v successfully updated LSA(%+v) with interface %v", a.AreaId, id, i.c.ifi.Name)
			} else {
				LogDebug("area %v successfully updated LSA(%+v)", a.AreaId, id)
			}
			a.ins.floodLSA(a, i, a.ins.RouterId, lsa.LSAheader)
		}
		return true
	}
	return false
}

func (a *Area) batchTryUpdatingExistingLSAs(lsas []packet2.LSAdvertisement, i *Interface, modFn func(idx int, lsa *packet2.LSAdvertisement)) (nonExists []packet2.LSAdvertisement) {
	var advLSAs []packet2.LSAheader
	for idx, l := range lsas {
		_, lsa, _, ok := a.lsDbGetLSAByIdentity(l.GetLSAIdentity(), true)
		if !ok {
			nonExists = append(nonExists, l)
			continue
		}
		modFn(idx, &lsa)
		// update LSA header for re-originating
		seqIncred := lsa.PrepareReOriginating(true)
		if err := lsa.FixLengthAndChkSum(); err != nil {
			if i != nil {
				LogErr("area %v err fix chkSum while updating LSA(%+v) with interface %v", a.AreaId, lsa.GetLSAIdentity(), i.c.ifi.Name)
			} else {
				LogErr("area %v err fix chkSum while updating LSA(%+v)", a.AreaId, l.GetLSAIdentity())
			}
			continue
		}
		// Check if the existing LSA exceed maxSeqNum.
		// This is done by checking seqIncrement failed and LSSeq >= MaxSequenceNumber
		if !seqIncred && int32(lsa.LSSeqNumber) >= packet2.MaxSequenceNumber {
			a.doLSASeqNumWrappingAndFloodNewLSA(lsa.GetLSAIdentity(), lsa)
			continue
		}
		if a.lsDbInstallNewLSA(lsa) {
			advLSAs = append(advLSAs, lsa.LSAheader)
		}
	}
	if len(advLSAs) > 0 {
		LogDebug("area %v successfully updated %d LSA", a.AreaId, len(advLSAs))
		a.ins.floodLSA(a, i, a.ins.RouterId, advLSAs...)
	}
	return
}

func (a *Area) doLSASeqNumWrappingAndFloodNewLSA(id packet2.LSAIdentity, newLSA packet2.LSAdvertisement) {
	// premature old LSA and flood it out
	a.prematureLSA(id)
	// As soon as this flood
	//            has been acknowledged by all adjacent neighbors, a new
	//            instance can be originated with sequence number of
	//            InitialSequenceNumber.
	a.pendingWrappingLSAsRw.Lock()
	defer a.pendingWrappingLSAsRw.Unlock()
	if a.pendingWrappingLSAs == nil {
		a.pendingWrappingLSAs = make(map[packet2.LSAIdentity]packet2.LSAdvertisement)
	}
	a.pendingWrappingLSAs[id] = newLSA
	if a.pendingWrappingLSAsTicker == nil {
		a.pendingWrappingLSAsTicker = TimeTickerFunc(a.ctx, time.Second, func() {
			if a.installAndFloodPendingLSA() <= 0 {
				a.pendingWrappingLSAsTicker.Suspend()
			}
		}, true)
	}
	a.pendingWrappingLSAsTicker.Reset()
}

func (a *Area) installAndFloodPendingLSA() (remainingLSACnt int) {
	a.pendingWrappingLSAsRw.Lock()
	defer a.pendingWrappingLSAsRw.Unlock()
	var (
		floodedLSAs []packet2.LSAIdentity
		originLen   = len(a.pendingWrappingLSAs)
	)
	for _, lsa := range a.pendingWrappingLSAs {
		stillNotAckedForPremature := false
		for _, i := range a.Interfaces {
			i.rangeOverNeighbors(func(nb *Neighbor) bool {
				if nb.isInLSRetransmissionList(lsa.GetLSAIdentity()) {
					// premature but still in retransmission list.
					stillNotAckedForPremature = true
					return false
				}
				return true
			})
			if stillNotAckedForPremature {
				break
			}
		}
		if !stillNotAckedForPremature {
			LogDebug("area %v LSA(%v) seqNum wrapping confirmed. originating new LSA", a.AreaId, lsa.GetLSAIdentity())
			// must re-init the LSSeqNumber
			lsa.LSSeqNumber = packet2.InitialSequenceNumber
			lsa.LSAge = 0
			a.originatingNewLSA(lsa)
			floodedLSAs = append(floodedLSAs, lsa.GetLSAIdentity())
		}
	}
	for _, id := range floodedLSAs {
		delete(a.pendingWrappingLSAs, id)
	}
	return originLen - len(floodedLSAs)
}

func (a *Area) prematureLSA(ids ...packet2.LSAIdentity) {
	var (
		allLSA []packet2.LSAdvertisement
		metas  []*lsaMeta
	)
	for _, id := range ids {
		_, lsa, meta, ok := a.lsDbGetLSAByIdentity(id, true)
		if !ok {
			LogWarn("area %v err premature LSA(%+v): LSA not found in LSDB", a.AreaId, id)
			continue
		}
		allLSA = append(allLSA, lsa)
		metas = append(metas, meta)
	}

	// step to premature
	// 0 stop LSDB aging (prevent it from been refreshed)
	// 1 set it age to maxAge, and set a marker to tell aging ticker do not refresh it
	// 2 install it backto LSDB
	// 3 continue LSDB aging
	// 4 flood it out
	a.ins.suspendAgingLSDB()
	var (
		err     error
		allLSAh []packet2.LSAheader
	)
	defer func() {
		if len(allLSAh) > 0 {
			a.ins.floodLSA(a, nil, a.ins.RouterId, allLSAh...)
			a.pendingRemoveMaturedTicker.Reset()
		}
	}()
	defer a.ins.continueAgingLSDB()

	a.pendingRemoveMaturedRw.Lock()
	defer a.pendingRemoveMaturedRw.Unlock()
	if a.pendingRemoveMaturedTicker == nil {
		a.pendingRemoveMaturedTicker = TimeTickerFunc(a.ctx, time.Second, func() {
			if a.removeMaturedLSAs() <= 0 {
				a.pendingRemoveMaturedTicker.Suspend()
			}
		}, true)
	}
	if a.pendingRemoveMaturedLSAs == nil {
		a.pendingRemoveMaturedLSAs = make(map[packet2.LSAIdentity]struct{})
	}
	for idx, lsa := range allLSA {
		meta := metas[idx]
		meta.premature()
		lsa.LSAge = packet2.MaxAge
		// since we just modified the LSAge field. chksum is still ok.
		if err = a.lsDbInstallLSA(lsa, meta); err != nil {
			LogErr("area %v err premature LSA(%+v)", a.AreaId, lsa.GetLSAIdentity())
			continue
		}
		LogDebug("area %v is pre-maturing LSA(%+v)", a.AreaId, lsa.GetLSAIdentity())
		allLSAh = append(allLSAh, lsa.LSAheader)
		a.pendingRemoveMaturedLSAs[lsa.GetLSAIdentity()] = struct{}{}
	}
}

func (a *Area) removeMaturedLSAs() (remainingLSACnt int) {
	a.pendingRemoveMaturedRw.Lock()
	defer a.pendingRemoveMaturedRw.Unlock()
	var (
		matureOK  []packet2.LSAIdentity
		originCnt = len(a.pendingRemoveMaturedLSAs)
	)
	for id := range a.pendingRemoveMaturedLSAs {
		existInLSRtxmList := false
		for _, ifi := range a.Interfaces {
			ifi.rangeOverNeighbors(func(nb *Neighbor) bool {
				if nb.isInLSRetransmissionList(id) {
					existInLSRtxmList = true
					return false
				}
				return true
			})
			if existInLSRtxmList {
				break
			}
		}
		if !existInLSRtxmList {
			matureOK = append(matureOK, id)
		}
	}
	for _, id := range matureOK {
		delete(a.pendingRemoveMaturedLSAs, id)
		if h, _, _, ok := a.lsDbGetLSAByIdentity(id, false); ok && h.LSAge == packet2.MaxAge {
			LogDebug("area %v successfully removed matured LSA(%+v)", a.AreaId, id)
			a.lsDbDeleteLSAByIdentity(id)
		}
	}
	return originCnt - len(matureOK)
}

func (a *Area) refreshSelfOriginatedLSA(id packet2.LSAIdentity) {
	_, lsa, _, ok := a.lsDbGetLSAByIdentity(id, true)
	if !ok {
		LogWarn("area %v err refresh self-originated LSA(%+v): previous LSA not found in LSDB", a.AreaId, id)
		return
	}
	lsa.LSAge = 0
	LogDebug("area %v refreshing self-originated LSA(%+v)", a.AreaId, id)
	a.originatingNewLSA(lsa)
}

func (a *Area) originatingNewLSA(lsa packet2.LSAdvertisement) {
	if err := lsa.FixLengthAndChkSum(); err != nil {
		LogErr("area %v err fix chkSum while originating new LSA(%+v)", a.AreaId, lsa.GetLSAIdentity())
		return
	}
	LogDebug("area %v originating new LSA: %+v", a.AreaId, lsa)
	if a.lsDbInstallNewLSA(lsa) {
		LogDebug("area %v successfully originated new LSA(%+v)", a.AreaId, lsa.GetLSAIdentity())
		a.ins.floodLSA(a, nil, a.ins.RouterId, lsa.LSAheader)
	}
}

func (a *Area) batchOriginatingNewLSAs(lsas []packet2.LSAdvertisement) {
	var advLSAs []packet2.LSAheader
	for _, lsa := range lsas {
		if err := lsa.FixLengthAndChkSum(); err != nil {
			LogErr("area %v err fix chkSum while originating new LSA(%+v)", a.AreaId, lsa.GetLSAIdentity())
			continue
		}
		if a.lsDbInstallNewLSA(lsa) {
			advLSAs = append(advLSAs, lsa.LSAheader)
		}
	}
	if len(advLSAs) > 0 {
		LogDebug("area %v successfully originated %d new LSAs", a.AreaId, len(advLSAs))
		a.ins.floodLSA(a, nil, a.ins.RouterId, advLSAs...)
	}
}

func (a *Area) updateLSDBWhenInterfaceAdd(i *Interface) {
	// need update RouterLSA when interface updated.

	if !a.tryUpdatingExistingLSA(packet2.LSAIdentity{
		LSType:      layers.RouterLSAtypeV2,
		LinkStateId: a.ins.RouterId,
		AdvRouter:   a.ins.RouterId,
	}, nil, func(lsa *packet2.LSAdvertisement) {
		LogDebug("updating self-originated RouterLSA with newly added interface %v", i.c.ifi.Name)
		// update existing LSA
		rtLSA, err := lsa.AsV2RouterLSA()
		if err != nil {
			LogErr("area %v err AsV2RouterLSA with newly added interface %v", a.AreaId, i.c.ifi.Name)
			return
		}
		rtLSA.Content.Routers = append(rtLSA.Content.Routers, packet2.RouterV2{
			RouterV2: layers.RouterV2{
				Type:     2,
				LinkID:   i.DR.Load(),
				LinkData: ipv4BytesToUint32(i.Address.IP.To4()),
				Metric:   10,
			},
		})
		rtLSA.Content.Links = uint16(len(rtLSA.Content.Routers))
		lsa.Content = rtLSA.Content
	}) {
		LogDebug("adding self-originated RouterLSA with newly added interface %v", i.c.ifi.Name)
		// LSA not found. originating a new one.
		a.originatingNewLSA(i.newRouterLSA())
	}
}

func (a *Area) announceASBR() {
	a.originatingNewLSA(packet2.LSAdvertisement{
		LSAheader: packet2.LSAheader{
			LSAge:       0,
			LSType:      layers.SummaryLSAASBRtypeV2,
			LinkStateID: a.ins.RouterId,
			AdvRouter:   a.ins.RouterId,
			LSSeqNumber: packet2.InitialSequenceNumber,
			LSOptions:   uint8(packet2.BitOption(0).SetBit(packet2.CapabilityEbit)),
		},
		Content: packet2.V2SummaryLSAImpl{
			NetworkMask: 0,
			Metric:      20,
		},
	})
}

func (a *Area) updateSelfOriginatedLSAWhenDRorBDRChanged(i *Interface) {
	// need update RouterLSA when DR updated.
	LogDebug("updating self-originated RouterLSA with new DR/BDR")
	if !a.tryUpdatingExistingLSA(packet2.LSAIdentity{
		LSType:      layers.RouterLSAtypeV2,
		LinkStateId: a.ins.RouterId,
		AdvRouter:   a.ins.RouterId,
	}, i, func(lsa *packet2.LSAdvertisement) {
		rtLSA, err := lsa.AsV2RouterLSA()
		if err != nil {
			LogErr("area %v err AsV2RouterLSA with changed DR/BDR on interface %v", a.AreaId, i.c.ifi.Name)
			return
		}
		for idx := 0; idx < len(rtLSA.Content.Routers); idx++ {
			rt := rtLSA.Content.Routers[idx]
			rt.LinkID = i.DR.Load()
			rtLSA.Content.Routers[idx] = rt
		}
		lsa.Content = rtLSA.Content
	}) {
		LogWarn("area %v err update RouterLSA with changed DR/BDR on interface %v: unexpected no existing routerLSA",
			a.AreaId, i.c.ifi.Name)
	}
}

func (a *Area) dealWithReceivedNewerSelfOriginatedLSA(fromIfi *Interface, newerReceivedLSA packet2.LSAdvertisement) {
	// It may be the case the router no longer wishes to originate the
	//        received LSA. Possible examples include: 1) the LSA is a
	//        summary-LSA or AS-external-LSA and the router no longer has an
	//        (advertisable) route to the destination, 2) the LSA is a
	//        network-LSA but the router is no longer Designated Router for
	//        the network or 3) the LSA is a network-LSA whose Link State ID
	//        is one of the router's own IP interface addresses but whose
	//        Advertising Router is not equal to the router's own Router ID
	//        (this latter case should be rare, and it indicates that the
	//        router's Router ID has changed since originating the LSA).  In
	//        all these cases, instead of updating the LSA, the LSA should be
	//        flushed from the routing domain by incrementing the received
	//        LSA's LS age to MaxAge and reflooding (see Section 14.1).
	LogDebug("area %v adapted newer self-originated LSA(%v) on interface %v. Trying incr its SeqNum and re-flood it out",
		a.AreaId, newerReceivedLSA.GetLSAIdentity(), fromIfi.c.ifi.Name)
	// TODO: check LSA type  and local LSDB to determine whether incr seqNum or premature it.
	// For now simply add the LSSeqNum and flood it out.
	if !a.tryUpdatingExistingLSA(newerReceivedLSA.GetLSAIdentity(), fromIfi, func(lsa *packet2.LSAdvertisement) {
		// it's already installed into LSDB.
		// and noop is ok.
	}) {
		LogWarn("area %v err incr LSSeqNum of received newer self-originated LSA on interface %v: "+
			"target LSA(%+v) not found in LSDB",
			a.AreaId, fromIfi.c.ifi.Name, newerReceivedLSA.GetLSAIdentity())
	}
}
