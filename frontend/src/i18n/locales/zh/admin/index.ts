import overview from './overview'
import channels from './channels'
import accounts from './accounts'
import resources from './resources'
import ops from './ops'
import settings from './settings'
import audit from './audit'
import plugins from './plugins'
import videoProtocolProfiles from './videoProtocolProfiles'
import apiDocsSettings from './apiDocsSettings'
import requestDataCacheSettings from './requestDataCacheSettings'

export default {
  ...overview,
  ...channels,
  ...accounts,
  ...resources,
  ...ops,
  ...settings,
  ...audit,
  ...plugins,
  ...videoProtocolProfiles,
  ...apiDocsSettings,
  ...requestDataCacheSettings,
}
