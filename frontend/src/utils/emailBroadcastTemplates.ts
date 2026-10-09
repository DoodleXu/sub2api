import type { EmailBroadcastTemplate } from '@/api/admin/settings'

// The marker survives template storage in message_html, keeping the QR editable.
export function getWechatQrUrl(html: string): string | null {
  const document = new DOMParser().parseFromString(html, 'text/html')
  return document.querySelector('img[data-wechat-qr], img[alt="微信群二维码，点击查看原图"]')?.getAttribute('src') ?? null
}

export function isValidWechatQrUrl(value: string): boolean {
  try { return ['http:', 'https:'].includes(new URL(value).protocol) }
  catch { return false }
}

export function replaceWechatQrUrl(html: string, url: string): string {
  const document = new DOMParser().parseFromString(html, 'text/html')
  const image = document.querySelector('img[data-wechat-qr], img[alt="微信群二维码，点击查看原图"]')
  if (!image) return html
  image.setAttribute('data-wechat-qr', '')
  image.setAttribute('src', url)
  image.closest('a')?.setAttribute('href', url)
  return document.body.innerHTML
}

// Content fragments fit the existing broadcast envelope, avoiding nested HTML documents.
export const rechargeBonusTemplate: EmailBroadcastTemplate = {
  id: 'builtin-recharge-bonus',
  name: '充值满赠活动提醒',
  updated_at: '',
  locale: 'zh',
  message_title: '充值满赠活动已开启，最高赠送 10% 额外余额',
  action_label: '前往充值',
  action_url: 'https://ai.clol.site',
  message_html: `<p style="font-size:16px;line-height:1.8;color:#475569;">充值满赠活动现已开启！充值达到以下档位，即可获得额外余额，充值越多，赠送比例越高。</p>
<table role="presentation" style="width:100%;border-collapse:collapse;margin:24px 0;background:#f0f6ff;color:#172033;text-align:center;">
<tr><td style="padding:20px 8px;border:1px solid #dbeafe;"><strong style="font-size:20px;color:#2563eb;">满 30 元</strong><br>赠送 2%</td><td style="padding:20px 8px;border:1px solid #dbeafe;"><strong style="font-size:20px;color:#2563eb;">满 50 元</strong><br>赠送 3%</td><td style="padding:20px 8px;border:1px solid #dbeafe;"><strong style="font-size:20px;color:#2563eb;">满 100 元</strong><br>赠送 6%</td></tr>
<tr><td style="padding:20px 8px;border:1px solid #dbeafe;"><strong style="font-size:20px;color:#2563eb;">满 200 元</strong><br>赠送 8%</td><td style="padding:20px 8px;border:1px solid #dbeafe;"><strong style="font-size:20px;color:#2563eb;">满 300 元</strong><br>赠送 9%</td><td style="padding:20px 8px;border:1px solid #dbeafe;"><strong style="font-size:20px;color:#2563eb;">满 500 元</strong><br>赠送 10%</td></tr>
</table>
<p style="padding:14px 16px;background:#fff9eb;border-left:4px solid #f0a400;line-height:1.8;color:#6d4a00;">充值满 30 元即可参与，满 500 元享 10% 额外余额。具体活动规则以充值页面实际展示为准。</p>
<h3 style="margin-top:28px;">请记住我们的域名</h3>
<p><a href="https://ai.clol.site" style="color:#2563eb;">https://ai.clol.site</a></p>
<h3 style="margin-top:28px;">加入微信群组</h3>
<p style="line-height:1.8;">扫码加入“唠唠的小店交流群”，获取活动通知与使用交流。</p>
<p style="text-align:center;"><a href="https://s3.laoooo.cn/2026/%E7%BE%A4%E8%81%8A%EF%BC%9A%E5%94%A0%E5%94%A0%E7%9A%84%E5%B0%8F%E5%BA%97%E4%BA%A4%E6%B5%81%E7%BE%A4.png"><img src="https://s3.laoooo.cn/2026/%E7%BE%A4%E8%81%8A%EF%BC%9A%E5%94%A0%E5%94%A0%E7%9A%84%E5%B0%8F%E5%BA%97%E4%BA%A4%E6%B5%81%E7%BE%A4.png" alt="微信群二维码，点击查看原图" width="220" style="max-width:100%;height:auto;border:0;"></a></p>
<p style="line-height:1.8;">感谢你的支持与使用，祝你使用愉快！</p>`,
}
