<template>
    <span class="tag_wrap">
        <span class="tag">{{ tag }}</span>
    </span>
</template>

<script setup lang="ts">
defineProps<{ tag: string }>()
</script>

<!--
  gkill 本体の付随タグの見た目を写したもの。
  向こうは文脈メニューや検索のハイライトを持つが、ここでは表示だけ。
-->
<style lang="css" scoped>
/* 枠はタグ同士の間隔を空けるためだけのもの。
   テーマ背景色で塗るとカード面の上で黒く浮くので透明にする */
.tag {
    border: solid transparent 2px;
    border-left: 0px;
    color: blue;
    padding: 0 6px 0 2px;
    font-size: small;
    border-radius: 0 1em 1em 0;
    display: inline-flex;
    background: lightgray;
}

/* タグ左端のパンチホール。穴に見せるため、常に背後の色と同じにする。 */
.tag::before {
    content: "・";
    color: rgb(var(--v-theme-background));
}
</style>
