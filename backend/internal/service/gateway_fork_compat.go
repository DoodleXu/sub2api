package service

func (s *GatewayService) LegacyLongContextRule(platform string) *LegacyLongContextRule {
	if s == nil || s.billingService == nil {
		return nil
	}
	return s.billingService.LegacyLongContextRule(platform)
}
