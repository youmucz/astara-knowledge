<template>
  <div
    id="weknora-embedded-root"
    :data-weknora-embedded="rootVersion"
    :theme-mode="theme"
    class="weknora-embedded-root"
  >
    <t-config-provider :globalConfig="tdGlobalConfig">
      <RouterView />
    </t-config-provider>
  </div>
</template>

<script setup lang="ts">
/**
 * Root component of the Plane-embedded Knowledge surface.
 *
 * Owns:
 *  - the versioned scoping attribute every embedded CSS rule is prefixed
 *    with (see vite.config.embedded.ts);
 *  - the tdesign global config (locale + portal attach so popups/dialogs
 *    render inside the body-level scoped portal, never on bare body);
 *  - the theme-mode token mapped from the Plane theme — applied to this
 *    root only, never to documentElement;
 *  - the capability context consumed by shared knowledge views.
 */
import { computed, provide, ref, watch } from 'vue'
import { RouterView } from 'vue-router'
import { useI18n } from 'vue-i18n'
import enUSConfig from 'tdesign-vue-next/esm/locale/en_US'
import zhCNConfig from 'tdesign-vue-next/esm/locale/zh_CN'
import ruRUConfig from 'tdesign-vue-next/esm/locale/ru_RU'
import koKRConfig from 'tdesign-vue-next/esm/locale/ko_KR'
import { EMBEDDED_ROOT_VALUE, EMBEDDED_PORTAL_SELECTOR, normalizeEmbeddedTheme } from './contract'
import { EMBEDDED_SURFACE_KEY, type EmbeddedSurfaceContext } from './surface'

defineOptions({ name: 'WeKnoraEmbeddedApp' })

const props = defineProps<{
  theme?: string
  capabilities?: readonly string[]
}>()

const rootVersion = EMBEDDED_ROOT_VALUE

const theme = computed(() => normalizeEmbeddedTheme(props.theme))
const capabilityList = ref<readonly string[]>(props.capabilities ?? [])

watch(
  () => props.capabilities,
  (next) => {
    capabilityList.value = next ?? []
  },
  { immediate: true },
)

provide(EMBEDDED_SURFACE_KEY, {
  capabilities: () => capabilityList.value,
} satisfies EmbeddedSurfaceContext)

const tdLocaleMap: Record<string, object> = {
  'en-US': enUSConfig,
  'zh-CN': zhCNConfig,
  'ru-RU': ruRUConfig,
  'ko-KR': koKRConfig,
}

const { locale } = useI18n()

const tdGlobalConfig = computed(() => ({
  ...tdLocaleMap[locale.value],
  // Route every tdesign popup (dialog/drawer/select/tooltip...) into the
  // portal root so styles and stacking stay inside the embedded surface.
  attach: EMBEDDED_PORTAL_SELECTOR,
}))
</script>

<style>
/* The embedded root fills the Plane content area. */
.weknora-embedded-root {
  height: 100%;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: var(--td-bg-color-page, #f5f5f5);
}
</style>
