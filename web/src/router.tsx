import { lazy } from 'react'
import { createBrowserRouter } from 'react-router-dom'
import { AdminLayout } from '@/layouts/AdminLayout'
import { ConsoleLayout } from '@/layouts/ConsoleLayout'
import { SiteLayout } from '@/layouts/SiteLayout'
import { RequireAuth } from '@/router/guards'

// Route-level code splitting keeps the first paint free of chart/admin code.
const LandingPage = lazy(() => import('@/pages/site/LandingPage'))
const PricingPage = lazy(() => import('@/pages/site/PricingPage'))
const DocsPage = lazy(() => import('@/pages/site/DocsPage'))
const LoginPage = lazy(() => import('@/pages/site/LoginPage'))
const IntegrationsPage = lazy(() => import('@/pages/site/IntegrationsPage'))
const TermsPage = lazy(() => import('@/pages/site/TermsPage'))
const PrivacyPage = lazy(() => import('@/pages/site/PrivacyPage'))
const NotFoundPage = lazy(() => import('@/pages/site/NotFoundPage'))

const ConsoleOverviewPage = lazy(() => import('@/pages/console/OverviewPage'))
const KeysPage = lazy(() => import('@/pages/console/KeysPage'))
const KeyDetailPage = lazy(() => import('@/pages/console/KeyDetailPage'))
const ConsoleModelsPage = lazy(() => import('@/pages/console/ModelsPage'))
const UsagePage = lazy(() => import('@/pages/console/UsagePage'))
const RechargePage = lazy(() => import('@/pages/console/RechargePage'))
const LogsPage = lazy(() => import('@/pages/console/LogsPage'))
const PlaygroundPage = lazy(() => import('@/pages/console/PlaygroundPage'))
const ConsoleTicketsPage = lazy(() => import('@/pages/console/TicketsPage'))

const AdminOverviewPage = lazy(() => import('@/pages/admin/OverviewPage'))
const ChannelsPage = lazy(() => import('@/pages/admin/ChannelsPage'))
const UsersPage = lazy(() => import('@/pages/admin/UsersPage'))
const AdminPricingPage = lazy(() => import('@/pages/admin/PricingPage'))
const AnnouncementsPage = lazy(() => import('@/pages/admin/AnnouncementsPage'))
const ReviewsPage = lazy(() => import('@/pages/admin/ReviewsPage'))
const AdminTicketsPage = lazy(() => import('@/pages/admin/TicketsPage'))
const AdminModelsPage = lazy(() => import('@/pages/admin/ModelsPage'))
const AdminOrdersPage = lazy(() => import('@/pages/admin/OrdersPage'))
const AdminRedeemCodesPage = lazy(() => import('@/pages/admin/RedeemCodesPage'))

export const router = createBrowserRouter([
  {
    element: <SiteLayout />,
    children: [
      { index: true, element: <LandingPage /> },
      { path: 'pricing', element: <PricingPage /> },
      { path: 'docs', element: <DocsPage /> },
      { path: 'login', element: <LoginPage /> },
      { path: 'integrations', element: <IntegrationsPage /> },
      // 法律条款占位页：挂在 SiteLayout 之下，自动获得页头页脚。
      { path: 'terms', element: <TermsPage /> },
      { path: 'privacy', element: <PrivacyPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
  {
    path: '/console',
    element: (
      <RequireAuth role="user">
        <ConsoleLayout />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <ConsoleOverviewPage /> },
      { path: 'keys', element: <KeysPage /> },
      { path: 'keys/:id', element: <KeyDetailPage /> },
      { path: 'models', element: <ConsoleModelsPage /> },
      { path: 'usage', element: <UsagePage /> },
      { path: 'recharge', element: <RechargePage /> },
      { path: 'logs', element: <LogsPage /> },
      { path: 'playground', element: <PlaygroundPage /> },
      { path: 'tickets', element: <ConsoleTicketsPage /> },
    ],
  },
  {
    path: '/admin',
    element: (
      <RequireAuth role="admin">
        <AdminLayout />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <AdminOverviewPage /> },
      { path: 'channels', element: <ChannelsPage /> },
      { path: 'users', element: <UsersPage /> },
      { path: 'pricing', element: <AdminPricingPage /> },
      { path: 'announcements', element: <AnnouncementsPage /> },
      { path: 'reviews', element: <ReviewsPage /> },
      { path: 'tickets', element: <AdminTicketsPage /> },
      { path: 'models', element: <AdminModelsPage /> },
      { path: 'orders', element: <AdminOrdersPage /> },
      { path: 'redeem-codes', element: <AdminRedeemCodesPage /> },
    ],
  },
])
