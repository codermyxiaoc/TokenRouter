import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import batchImage from './batchImage'
import creative from './creative'
import admin from './admin'
import misc from './misc'
import team from './team'
import tickets from './tickets'
import mediaTasks from './mediaTasks'

export default {
  ...landing,
  ...common,
  ...dashboard,
  ...batchImage,
  ...creative,
  admin,
  ...misc,
  ...team,
  ...tickets,
  ...mediaTasks,
}
