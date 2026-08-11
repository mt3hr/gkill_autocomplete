<template>
    <div class="camera-wrap">
        <div ref="star_field" class="star-field"></div>
    </div>
</template>

<script setup lang="ts">
import { useSaihateStarsOverlay } from '@/classes/use-saihate-stars-overlay'

const { star_field } = useSaihateStarsOverlay()
</script>

<!--
  scoped にしてはいけない。星は JavaScript で作った div なので、
  scoped が付ける属性が乗らず、指定が当たらなくなる。
-->
<style>
.camera-wrap {
    position: fixed;
    top: 0;
    left: 0;
    width: 100vw;
    height: 100vh;
    pointer-events: none;
    animation: cameraShake 10s infinite ease-in-out;
    overflow: hidden;
    z-index: -100000000;
}

@keyframes cameraShake {
    0% { transform: translate(0px, 0px); }
    25% { transform: translate(1px, -1px); }
    50% { transform: translate(-1px, 1px); }
    75% { transform: translate(1px, 0px); }
    100% { transform: translate(0px, 0px); }
}

.star-field {
    position: absolute;
    width: 100%;
    height: 100%;
    top: 0;
    left: 0;
}

.background-star {
    position: absolute;
    width: 2px;
    height: 2px;
    background: white;
    border-radius: 50%;
    animation: twinkle 2s infinite;
    opacity: 0.8;
}

@keyframes twinkle {
    0%, 100% { opacity: 0.3; }
    50% { opacity: 1; }
}

.red-star {
    background: red;
    width: 3px;
    height: 3px;
    animation: twinkleRed 3s infinite;
}

@keyframes twinkleRed {
    0%, 100% { opacity: 0.1; }
    50% { opacity: 0.9; }
}

.big-star {
    width: 4px;
    height: 4px;
    animation: twinkleBig 4s infinite;
}

@keyframes twinkleBig {
    0%, 100% { opacity: 0.5; }
    50% { opacity: 1; }
}

.blue-star {
    background: #66ccff;
    width: 3px;
    height: 3px;
    animation: twinkleBlue 3.5s infinite;
}

@keyframes twinkleBlue {
    0%, 100% { opacity: 0.2; }
    50% { opacity: 0.8; }
}

.shooting-star {
    animation: shooting 1s ease-out forwards;
}

@keyframes shooting {
    0% { opacity: 1; transform: translate(0, 0) rotate(135deg); }
    100% { opacity: 0; transform: translate(-500px, 500px) rotate(135deg); }
}

/* 動きを減らす設定のときは瞬きも止める。 */
@media (prefers-reduced-motion: reduce) {
    .camera-wrap,
    .background-star {
        animation: none;
    }
}
</style>
